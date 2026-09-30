package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	ed2k "github.com/goed2k/core"
	"github.com/goed2k/core/bootstrap"
	"github.com/p2psearch/p2psearch/internal/config"
	"github.com/p2psearch/p2psearch/internal/proxyutil"
)

var (
	ErrBusy       = errors.New("another search is in progress")
	ErrEmptyQuery = errors.New("query is empty")
	ErrNotReady   = errors.New("ed2k engine is not ready")
)

// Result is a search hit exposed to the HTTP/API layer.
type Result struct {
	FileName        string `json:"filename"`
	FileSize        int64  `json:"size"`
	Hash            string `json:"hash"`
	Sources         int    `json:"sources"`
	CompleteSources int    `json:"complete_sources"`
	Extension       string `json:"extension,omitempty"`
	FileType        string `json:"filetype,omitempty"`
	ED2K            string `json:"ed2k"`
	Source          string `json:"source"` // server / kad / both
}

// SearchResponse is returned by Search.
type SearchResponse struct {
	Query     string   `json:"query"`
	State     string   `json:"state"`
	Count     int      `json:"count"`
	Limit     int      `json:"limit"`
	ElapsedMs int64    `json:"elapsed_ms"`
	Error     string   `json:"error,omitempty"`
	Results   []Result `json:"results"`
	Strategy  string   `json:"strategy,omitempty"` // local+global+kad | failover+backup
	Failover  bool     `json:"failover,omitempty"`
}

// Status is a lightweight health snapshot.
type Status struct {
	Ready             bool     `json:"ready"`
	Servers           string   `json:"servers,omitempty"`
	ConnectedServers  []string `json:"connected_servers,omitempty"`
	ConnectedCount    int      `json:"connected_count"`
	ConfiguredCount   int      `json:"configured_count"`
	DHTEnabled        bool     `json:"dht_enabled"`
	DHTNodes          int      `json:"dht_nodes,omitempty"`
	Searching         bool     `json:"searching"`
	Proxy             string   `json:"proxy,omitempty"`
	SearchViaProxy    bool     `json:"search_via_proxy"`
}

// SearchOpts controls a single search call.
type SearchOpts struct {
	Wait  time.Duration
	Limit int
}

// Engine wraps goed2k for search-only use.
type Engine struct {
	client *ed2k.Client
	app    *config.App
	log    *slog.Logger

	mu        sync.Mutex
	searching bool
	stopCh    chan struct{}
	wg        sync.WaitGroup

	// discovered holds server.met addresses ranked by file count, refreshed at
	// bootstrap. Keeping it here means a search never touches the network just
	// to widen its server pool.
	discMu     sync.RWMutex
	discovered []string

	// health tracks handshake success/timeout per server. High-Files servers are
	// demoted on failure, never deleted from config (see server_health.go).
	health *serverHealthMap
}

// Start boots the ed2k client and begins server/KAD bootstrap.
func Start(app *config.App, log *slog.Logger) (*Engine, error) {
	if log == nil {
		log = slog.Default()
	}
	if app == nil {
		def := config.Default()
		app = &def
	}
	bcfg := app.BootstrapConfig()
	client, err := bootstrap.InitClient(bcfg, log)
	if err != nil {
		return nil, err
	}
	// KAD only via shared bootstrap helper; servers are connected by us.
	bootstrap.RunBackground(client, bcfg, func(format string, args ...any) {
		log.Warn(fmt.Sprintf(format, args...))
	})

	e := &Engine{
		client: client,
		app:    app,
		log:    log,
		stopCh: make(chan struct{}),
		health: newServerHealthMap(),
	}
	e.wg.Add(1)
	go e.refreshLoop()
	go e.bootstrapServers(false)
	return e, nil
}

// Close stops the refresh loop and underlying client.
func (e *Engine) Close() {
	if e == nil {
		return
	}
	select {
	case <-e.stopCh:
	default:
		close(e.stopCh)
	}
	e.wg.Wait()
	if e.client != nil {
		e.client.Close()
	}
}

// Status returns readiness info for /api/status.
func (e *Engine) Status() Status {
	if e == nil || e.client == nil {
		return Status{Ready: false}
	}
	e.mu.Lock()
	searching := e.searching
	e.mu.Unlock()

	connected := e.connectedServerAddrs()
	st := Status{
		Ready:            true,
		Servers:          strings.Join(connected, ","),
		ConnectedServers: connected,
		ConnectedCount:   len(connected),
		ConfiguredCount:  len(e.client.ServerStatuses()),
		DHTEnabled:       e.app.EnableKAD,
		Searching:        searching,
		Proxy:            proxyutil.RedactString(e.app.ProxyURL),
		SearchViaProxy:   proxyutil.SearchTunneled(),
	}
	if e.app.EnableKAD {
		st.DHTNodes = e.client.DHTStatus().KnownNodes
	}
	return st
}

func (e *Engine) connectedServerAddrs() []string {
	snaps := e.client.ServerStatuses()
	out := make([]string, 0, len(snaps))
	for _, s := range snaps {
		if !s.HandshakeCompleted {
			continue
		}
		addr := s.Address
		if addr == "" {
			addr = s.Identifier
		}
		out = append(out, addr)
	}
	return out
}

// SearchProgress is a partial snapshot pushed to a streaming client while a
// search is still running. Delta carries only results the client has not seen
// yet; Count is the running total of unique results found so far.
type SearchProgress struct {
	Query     string   `json:"query"`
	Phase     string   `json:"phase"`
	State     string   `json:"state"`
	Delta     []Result `json:"delta,omitempty"`
	Count     int      `json:"count"`
	Limit     int      `json:"limit"`
	ElapsedMs int64    `json:"elapsed_ms"`
	Strategy  string   `json:"strategy,omitempty"`
	Failover  bool     `json:"failover,omitempty"`
}

const (
	// streamEmitInterval coalesces result frames: new hits are pushed at most
	// this often, so a fast stream does not become a flood of tiny SSE frames.
	streamEmitInterval = 200 * time.Millisecond
	// streamHeartbeatInterval keeps a frame flowing even when no new result
	// arrived, so the client's elapsed timer never looks frozen.
	streamHeartbeatInterval = 700 * time.Millisecond
)

// Search runs one ed2k/KAD search and waits until finished or timeout.
// Strategy: concurrent search on priority servers, KAD as supplement;
// stream partial hits immediately; keep searching for the full wait budget
// (with backup enrich passes) so late unique results still arrive.
func (e *Engine) Search(ctx context.Context, query string, opts SearchOpts) (SearchResponse, error) {
	return e.search(ctx, query, opts, nil)
}

// SearchStream is Search with incremental progress. emit is invoked on the
// search goroutine for every frame; returning an error from it aborts the
// search early, which is how an SSE client hanging up tears the search down.
func (e *Engine) SearchStream(ctx context.Context, query string, opts SearchOpts, emit func(SearchProgress) error) (SearchResponse, error) {
	return e.search(ctx, query, opts, emit)
}

func (e *Engine) search(ctx context.Context, query string, opts SearchOpts, emit func(SearchProgress) error) (SearchResponse, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchResponse{}, ErrEmptyQuery
	}
	if e == nil || e.client == nil {
		return SearchResponse{}, ErrNotReady
	}

	wait := opts.Wait
	if wait <= 0 {
		wait = e.app.DefaultWait
	}
	if wait > 5*time.Minute {
		wait = 5 * time.Minute
	}
	limit := e.app.ClampLimit(opts.Limit)

	e.mu.Lock()
	if e.searching {
		e.mu.Unlock()
		return SearchResponse{}, ErrBusy
	}
	e.searching = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.searching = false
		e.mu.Unlock()
	}()

	_ = e.client.StopSearch()
	started := time.Now()

	emitter := newProgressEmitter(emit, query, limit, started)
	onSnap := func(cur ed2k.SearchSnapshot) { emitter.push(cur, time.Now()) }

	priority := e.app.PrioritySlice()
	warmDeadline := time.Now().Add(searchWarmupBudget(wait))
	e.ensureConnected(priority, "priority", warmDeadline)

	// Widen the server pool up-front so the first search already reaches backups.
	if backups := e.pickBackupServers(); len(backups) > 0 {
		e.connectMany(backups, warmDeadline)
	}

	scope := ed2k.SearchScopeServer | ed2k.SearchScopeGlobal
	if e.app.EnableKAD {
		scope |= ed2k.SearchScopeDHT
	}

	var snap ed2k.SearchSnapshot

	totalConnected := len(e.connectedServerAddrs())
	if totalConnected == 0 {
		e.log.Info("no servers online after widen", "priority", len(priority))
		emitter.markNoServers()
	}

	globalTargets := e.globalSearchTargets(28, 0)
	handle, err := e.client.StartSearch(ed2k.SearchParams{
		Query:         query,
		Scope:         scope,
		Charset:       "utf8", // public servers mostly match UTF-8; GBK is a CJK enrich pass
		GlobalServers: globalTargets,
		GlobalMax:     28,
	})
	if err != nil {
		return SearchResponse{}, err
	}
	e.log.Info("search started",
		"query", query,
		"tcp_servers", totalConnected,
		"global_udp", len(globalTargets),
		"kad", e.app.EnableKAD,
	)

	// First pass: stream hits live for the full wait budget (no settle early-exit).
	snap = e.waitSearch(ctx, handle, wait, limit, onSnap)
	if emitter.failed() {
		_ = handle.Stop()
		return SearchResponse{}, emitter.errValue()
	}
	if ctx.Err() != nil {
		_ = handle.Stop()
		return SearchResponse{}, ctx.Err()
	}

	// Charset rotation: UTF-8 first (above). For CJK, also try GBK once — some
	// Chinese-indexed shares only answer GBK. Never lead with GBK: it collapses
	// hit counts on typical EU/US servers.
	charsets := []string{}
	if containsCJK(query) {
		charsets = []string{"gbk"}
	}

	// Keep searching while wait remains. Servers often finish their first batch
	// early; reconnecting backups and re-querying fills in late/unique hits.
	const maxEnrichPasses = 3
	charsetIdx := 0
	for pass := 0; pass < maxEnrichPasses; pass++ {
		remain := wait - time.Since(started)
		if remain < 4*time.Second {
			break
		}
		if limit > 0 && len(snap.Results) >= limit {
			break
		}
		if snap.State == ed2k.SearchStateFailed && len(snap.Results) == 0 && pass == 0 {
			break
		}

		backups := e.pickBackupServers()
		if len(backups) == 0 {
			// Pool is at MaxTotalServers — free non-priority slots and pull
			// fresh server.met indexes so enrich is not a duplicate re-query.
			freed := e.rotateNonPriorityServers(6)
			if freed > 0 {
				backups = e.pickBackupServers()
			}
		}
		if len(backups) > 0 {
			e.log.Info("search enrich pass",
				"query", query,
				"pass", pass+1,
				"have", len(snap.Results),
				"connected", len(e.connectedServerAddrs()),
				"backups", len(backups),
				"remain_ms", remain.Milliseconds(),
			)
			enrichDeadline := time.Now().Add(searchWarmupBudget(remain))
			e.connectMany(backups, enrichDeadline)
		} else if pass > 0 && charsetIdx >= len(charsets) {
			break
		}

		charset := "utf8"
		if charsetIdx < len(charsets) {
			charset = charsets[charsetIdx]
			charsetIdx++
		}

		prev := snap
		_ = handle.Stop()
		time.Sleep(60 * time.Millisecond)

		emitter.enterEnrich()

		globTargets := e.globalSearchTargets(28, (pass+1)*24)
		handle, err = e.client.StartSearch(ed2k.SearchParams{
			Query:         query,
			Scope:         scope,
			Charset:       charset,
			GlobalServers: globTargets,
			GlobalMax:     28,
		})
		if err != nil {
			return SearchResponse{}, err
		}
		remain = wait - time.Since(started)
		if remain < 3*time.Second {
			remain = 3 * time.Second
		}
		before := len(prev.Results)
		snap = e.waitSearch(ctx, handle, remain, limit, onSnap)
		if emitter.failed() {
			_ = handle.Stop()
			return SearchResponse{}, emitter.errValue()
		}
		if ctx.Err() != nil {
			_ = handle.Stop()
			return SearchResponse{}, ctx.Err()
		}
		snap = mergeSearchSnapshots(prev, snap)
		if len(snap.Results) <= before && charsetIdx >= len(charsets) && len(backups) == 0 {
			e.log.Info("search enrich plateau", "have", len(snap.Results), "pass", pass+1)
			break
		}
	}

	results := make([]Result, 0, len(snap.Results))
	for _, r := range snap.Results {
		results = append(results, toResult(r))
		if len(results) >= limit {
			break
		}
	}

	strategy, failover := emitter.strategyLabel()
	resp := SearchResponse{
		Query:     query,
		State:     string(snap.State),
		Count:     len(results),
		Limit:     limit,
		ElapsedMs: time.Since(started).Milliseconds(),
		Error:     snap.Error,
		Results:   results,
		Strategy:  strategy,
		Failover:  failover,
	}
	e.log.Info("search finished",
		"query", query,
		"count", resp.Count,
		"limit", limit,
		"state", resp.State,
		"strategy", strategy,
		"failover", failover,
		"elapsed_ms", resp.ElapsedMs,
		"servers", e.Status().ConnectedCount,
	)
	return resp, nil
}

// waitSearch polls the search handle until the natural end or the wait budget.
//
// It does NOT settle-exit when the stream goes quiet early in the TCP phase:
// late server batches still matter. Once every TCP server has reported (or the
// server hard-ceiling fired) and the unique-hit count has been flat for a
// couple of seconds, it returns early so the caller can spend the remaining
// wait on enrich rotation — otherwise Global/KAD keep the handle "RUNNING"
// for the full budget while the UI shows a frozen count.
//
// Stop conditions: Finished/Stopped/Failed, explicit limit, ctx cancel, budget,
// or post-TCP plateau (see above).
func (e *Engine) waitSearch(ctx context.Context, handle ed2k.SearchHandle, budget time.Duration, limit int, onSnap func(ed2k.SearchSnapshot)) ed2k.SearchSnapshot {
	deadline := time.Now().Add(budget)
	started := time.Now()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	report := func(s ed2k.SearchSnapshot) {
		if onSnap != nil {
			onSnap(s)
		}
	}

	var snap ed2k.SearchSnapshot
	lastCount := -1
	var lastGrowth time.Time
	tcpDoneSeen := false
	var tcpDoneAt time.Time

	for {
		snap = handle.Snapshot()
		report(snap)
		n := len(snap.Results)

		if n != lastCount {
			if n > lastCount {
				lastGrowth = time.Now()
			}
			lastCount = n
		}

		if snap.State == ed2k.SearchStateFinished ||
			snap.State == ed2k.SearchStateStopped ||
			snap.State == ed2k.SearchStateFailed {
			break
		}
		if limit > 0 && n >= limit {
			_ = handle.Stop()
			break
		}

		// TCP fan-out complete (or never started): remember when the phase
		// went idle so we can plateau-exit while Global/KAD are still open.
		if !snap.ServerBusy {
			if !tcpDoneSeen {
				tcpDoneSeen = true
				tcpDoneAt = time.Now()
			}
		} else {
			tcpDoneSeen = false
		}

		select {
		case <-ctx.Done():
			_ = handle.Stop()
			final := handle.Snapshot()
			report(final)
			return final
		case <-ticker.C:
			now := time.Now()
			if now.After(deadline) {
				_ = handle.Stop()
				time.Sleep(120 * time.Millisecond)
				latest := handle.Snapshot()
				if len(latest.Results) >= len(snap.Results) {
					snap = latest
				}
				report(snap)
				return snap
			}
			// Plateau after TCP: leave budget for enrich. Require a short
			// grace so a slow first Global/KAD batch can still land.
			if tcpDoneSeen && n > 0 &&
				now.Sub(started) >= 4*time.Second &&
				now.Sub(tcpDoneAt) >= 1500*time.Millisecond &&
				!lastGrowth.IsZero() && now.Sub(lastGrowth) >= 2500*time.Millisecond &&
				(snap.GlobalBusy || snap.DHTBusy) {
				e.log.Info("search plateau after TCP — handing rest to enrich",
					"have", n,
					"elapsed_ms", now.Sub(started).Milliseconds(),
					"global", snap.GlobalBusy,
					"kad", snap.DHTBusy,
				)
				_ = handle.Stop()
				time.Sleep(80 * time.Millisecond)
				latest := handle.Snapshot()
				if len(latest.Results) >= len(snap.Results) {
					snap = latest
				}
				report(snap)
				return snap
			}
		}
	}
	return snap
}

// waitForAnyServer was removed: the warm-up windows inside connectMany already
// give a cold pool its grace period, and waiting again afterwards only added
// fixed latency when the pool was unreachable.

// mergeSearchSnapshots unions two result sets by file hash, keeping the richer sources count.
func mergeSearchSnapshots(a, b ed2k.SearchSnapshot) ed2k.SearchSnapshot {
	out := b
	if len(a.Results) == 0 {
		return out
	}
	if len(b.Results) == 0 {
		out = a
		return out
	}
	byHash := make(map[string]ed2k.SearchResult, len(a.Results)+len(b.Results))
	order := make([]string, 0, len(a.Results)+len(b.Results))
	add := func(r ed2k.SearchResult) {
		key := resultKey(r)
		if prev, ok := byHash[key]; ok {
			if r.Sources > prev.Sources {
				prev.Sources = r.Sources
			}
			if r.CompleteSources > prev.CompleteSources {
				prev.CompleteSources = r.CompleteSources
			}
			prev.Source |= r.Source
			byHash[key] = prev
			return
		}
		byHash[key] = r
		order = append(order, key)
	}
	for _, r := range a.Results {
		add(r)
	}
	for _, r := range b.Results {
		add(r)
	}
	merged := make([]ed2k.SearchResult, 0, len(order))
	for _, key := range order {
		merged = append(merged, byHash[key])
	}
	out.Results = merged
	if a.Error != "" && out.Error == "" {
		out.Error = a.Error
	}
	return out
}

func (e *Engine) shouldFailover(snap ed2k.SearchSnapshot, priority []string) bool {
	if snap.State == ed2k.SearchStateFailed {
		return true
	}
	if e.countConnected(priority) == 0 {
		return true
	}
	if len(snap.Results) == 0 {
		return true
	}
	return false
}

func (e *Engine) countConnected(addrs []string) int {
	connected := e.connectedSet()
	n := 0
	for _, a := range addrs {
		if _, ok := connected[strings.TrimSpace(a)]; ok {
			n++
		}
	}
	return n
}

func (e *Engine) connectedSet() map[string]struct{} {
	set := make(map[string]struct{})
	for _, a := range e.connectedServerAddrs() {
		set[a] = struct{}{}
	}
	return set
}

func (e *Engine) ensureConnected(addrs []string, label string, deadline time.Time) {
	connected := e.connectedSet()
	missing := make([]string, 0, len(addrs))
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if _, ok := connected[a]; !ok {
			missing = append(missing, a)
		}
	}
	if len(missing) == 0 {
		return
	}
	e.log.Info("ensuring servers connected", "role", label, "missing", len(missing))
	e.connectMany(missing, deadline)
}

// pickBackupServers returns unused backup addresses up to the MaxTotalServers
// cap, preferring server.met entries ranked by file count (bigger indexes) and
// falling back to the static all_servers list. Handshake-timeout servers are
// tried only after healthier ones (never removed from the met ranking).
//
// It performs no network I/O: the ranked list is cached by bootstrapServers.
// Returning nil once the pool is full is deliberate — the previous behaviour of
// always returning another batch made the connection pool grow on every search.
func (e *Engine) pickBackupServers() []string {
	connected := e.connectedSet()
	need := e.app.MaxTotalServers - len(connected)
	if need <= 0 {
		return nil
	}

	priority := make(map[string]struct{})
	for _, a := range e.app.PrioritySlice() {
		priority[a] = struct{}{}
	}

	seen := make(map[string]struct{}, len(connected)+len(e.app.AllServers)+32)
	for a := range connected {
		seen[a] = struct{}{}
	}
	for a := range priority {
		seen[a] = struct{}{}
	}

	e.discMu.RLock()
	discovered := append([]string(nil), e.discovered...)
	e.discMu.RUnlock()

	candidates := make([]string, 0, len(discovered)+len(e.app.AllServers))
	addCand := func(addr string) {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			return
		}
		if _, ok := seen[addr]; ok {
			return
		}
		seen[addr] = struct{}{}
		candidates = append(candidates, addr)
	}
	for _, addr := range discovered {
		addCand(addr)
	}
	for _, addr := range e.app.AllServers {
		addCand(addr)
	}

	// Stable partition: allowed (healthy / retry-due) first, demoted later.
	primary, demoted := make([]string, 0, len(candidates)), make([]string, 0, len(candidates))
	for _, addr := range candidates {
		if e.health != nil && !e.health.AllowConnect(addr) {
			demoted = append(demoted, addr)
			continue
		}
		primary = append(primary, addr)
	}
	if e.health != nil {
		sort.SliceStable(primary, func(i, j int) bool {
			return e.health.SortKey(primary[i]) < e.health.SortKey(primary[j])
		})
		sort.SliceStable(demoted, func(i, j int) bool {
			return e.health.SortKey(demoted[i]) < e.health.SortKey(demoted[j])
		})
	}

	out := make([]string, 0, need)
	for _, addr := range primary {
		out = append(out, addr)
		if len(out) >= need {
			return out
		}
	}
	for _, addr := range demoted {
		out = append(out, addr)
		if len(out) >= need {
			break
		}
	}
	return out
}

// rotateNonPriorityServers disconnects up to `want` non-priority TCP servers so
// pickBackupServers can refill from server.met. Priority servers are never
// dropped — they remain the fast path for the next search pass.
func (e *Engine) rotateNonPriorityServers(want int) int {
	if e == nil || e.client == nil || want <= 0 {
		return 0
	}
	priority := make(map[string]struct{})
	for _, a := range e.app.PrioritySlice() {
		priority[strings.TrimSpace(a)] = struct{}{}
	}
	connected := e.connectedServerAddrs()
	drop := make([]string, 0, want)
	for _, addr := range connected {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		if _, keep := priority[addr]; keep {
			continue
		}
		drop = append(drop, addr)
		if len(drop) >= want {
			break
		}
	}
	for _, addr := range drop {
		e.client.DisconnectServer(addr)
		e.log.Info("enrich rotate: disconnected", "server", addr)
	}
	return len(drop)
}

// refreshDiscoveredServers downloads server.met and caches the addresses ranked
// by file count. Called from bootstrap so a search never pays for the download.
func (e *Engine) refreshDiscoveredServers() {
	if len(e.app.ServerMetURLs) == 0 {
		return
	}
	list := e.ListServerMetAddresses(e.app.ServerMetURLs, 100)
	if list.Count == 0 {
		e.log.Warn("server.met produced no usable servers", "sources", len(e.app.ServerMetURLs))
		return
	}
	e.discMu.Lock()
	e.discovered = list.Addresses
	e.discMu.Unlock()
	e.log.Info("server.met cached", "servers", list.Count, "sources", len(e.app.ServerMetURLs))
}

func (e *Engine) refreshLoop() {
	defer e.wg.Done()
	interval := e.app.RefreshInterval
	if interval <= 0 {
		interval = 8 * time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-e.stopCh:
			return
		case <-ticker.C:
			e.log.Info("periodic priority server re-bootstrap")
			e.bootstrapServers(true)
		}
	}
}

// bootstrapServers connects priority + backup servers so the first search is wide.
func (e *Engine) bootstrapServers(refresh bool) {
	priority := e.app.PrioritySlice()
	e.log.Info("connecting priority servers", "count", len(priority), "refresh", refresh)
	e.connectMany(priority, time.Time{})

	// Populate the ranked server pool once, off the search path.
	e.refreshDiscoveredServers()

	if backups := e.pickBackupServers(); len(backups) > 0 {
		e.log.Info("pre-warming backup servers", "count", len(backups))
		e.connectMany(backups, time.Time{})
	}
	e.logConnected()
}

// ApplyLive applies config changes that can take effect without process restart.
// Returns human-readable applied actions and fields that still need a restart.
func (e *Engine) ApplyLive(before config.App) (applied []string, needRestart []string) {
	if e == nil || e.app == nil {
		return nil, nil
	}
	after := e.app

	if before.ListenPort != after.ListenPort {
		needRestart = append(needRestart, "TCP 端口")
	}
	if before.UDPPort != after.UDPPort {
		needRestart = append(needRestart, "UDP 端口")
	}
	if before.EnableKAD != after.EnableKAD {
		needRestart = append(needRestart, "KAD")
	}
	if before.EnableUPnP != after.EnableUPnP {
		needRestart = append(needRestart, "UPnP")
	}

	if before.ProxyURL != after.ProxyURL {
		if redacted, err := proxyutil.Configure(after.ProxyURL); err != nil {
			e.log.Error("hot-apply proxy failed", "err", err)
		} else {
			ed2k.DialTCP = proxyutil.ED2KDialTCP
			if redacted == "" {
				applied = append(applied, "代理（已关闭）")
			} else {
				applied = append(applied, "代理")
			}
			e.log.Info("proxy hot-applied", "proxy", redacted)
		}
	}

	serversChanged := before.MaxPriorityServers != after.MaxPriorityServers ||
		before.MaxTotalServers != after.MaxTotalServers ||
		!equalStringSlices(before.Servers, after.Servers) ||
		!equalStringSlices(before.AllServers, after.AllServers) ||
		!equalStringSlices(before.ServerMetURLs, after.ServerMetURLs)
	if serversChanged {
		go e.bootstrapServers(true)
		applied = append(applied, "服务器连接")
	}

	if !equalStringSlices(before.NodesDatURLs, after.NodesDatURLs) && after.EnableKAD {
		urls := append([]string(nil), after.NodesDatURLs...)
		go func() {
			if err := e.client.LoadDHTNodesDat(urls...); err != nil {
				e.log.Warn("hot-load nodes.dat failed", "err", err)
				return
			}
			e.log.Info("nodes.dat hot-loaded", "sources", len(urls))
		}()
		applied = append(applied, "nodes.dat")
	}

	if before.DefaultWait != after.DefaultWait ||
		before.DefaultLimit != after.DefaultLimit ||
		before.MaxLimit != after.MaxLimit {
		applied = append(applied, "搜索默认值")
	}
	if before.RefreshInterval != after.RefreshInterval {
		applied = append(applied, "刷新间隔（下次周期起效）")
	}
	if before.Offline115.Enabled != after.Offline115.Enabled ||
		before.Offline115.Cookie != after.Offline115.Cookie ||
		before.Offline115.WPPathID != after.Offline115.WPPathID {
		applied = append(applied, "115 网盘")
	}

	return applied, needRestart
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// searchWarmupBudget bounds the connection warm-up that runs before a search
// starts. A pool that cannot be reached must not consume the caller's wait: with
// no backends the search fails immediately, so every second spent waiting for a
// handshake that will never land is pure latency added to the response.
//
// A healthy pool costs almost nothing here — connectMany returns as soon as a
// new handshake completes — so the budget only ever bites on a dead pool.
func searchWarmupBudget(wait time.Duration) time.Duration {
	budget := wait / 3
	if budget > 2*time.Second {
		budget = 2 * time.Second
	}
	if budget < 300*time.Millisecond {
		budget = 300 * time.Millisecond
	}
	return budget
}

// connectMany dials every address in parallel, then waits a bounded moment for
// the handshakes so callers see a populated pool. When deadline is set the wait
// is capped by it; the zero value means "use the default window".
func (e *Engine) connectMany(addrs []string, deadline time.Time) {
	if len(addrs) == 0 {
		return
	}
	before := len(e.connectedServerAddrs())
	attempted := make([]string, 0, len(addrs))
	var wg sync.WaitGroup
	for _, addr := range addrs {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		if e.health != nil && !e.health.AllowConnect(addr) {
			e.log.Info("server dial skipped (backoff)", "server", addr)
			continue
		}
		attempted = append(attempted, addr)
		wg.Add(1)
		go func(serverAddr string) {
			defer wg.Done()
			if err := e.client.Connect(serverAddr); err != nil {
				e.log.Warn("server connect failed", "server", serverAddr, "err", err)
				if e.health != nil {
					e.health.MarkTCPFail(serverAddr)
				}
				return
			}
			e.log.Info("server connect started", "server", serverAddr)
		}(addr)
	}
	wg.Wait()

	// Wait briefly for handshakes. Prefer seeing *new* connections when widening.
	window := 2 * time.Second
	if before > 0 {
		window = 1200 * time.Millisecond
	}
	if !deadline.IsZero() {
		if remaining := time.Until(deadline); remaining < window {
			window = remaining
		}
	}
	waitedFull := true
	if window > 0 {
		end := time.Now().Add(window)
		for time.Now().Before(end) {
			n := len(e.connectedServerAddrs())
			if before == 0 && n > 0 {
				waitedFull = false
				break
			}
			if before > 0 && n > before {
				time.Sleep(200 * time.Millisecond)
				waitedFull = false
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Record handshake outcomes. Only demote on a full wait — early exit means
	// other servers may still be handshaking (don't punish high-Files like Sunrise).
	if e.health != nil && len(attempted) > 0 {
		ok := e.connectedSet()
		for _, addr := range attempted {
			if _, yes := ok[addr]; yes {
				e.health.MarkOK(addr)
			} else if waitedFull {
				e.health.MarkHandshakeTimeout(addr)
				e.log.Info("server handshake timeout — demoted, will retry later", "server", addr)
			}
		}
	}
}

func (e *Engine) logConnected() {
	connected := e.connectedServerAddrs()
	e.log.Info("ed2k servers ready",
		"connected", len(connected),
		"target", e.app.MaxTotalServers,
		"servers", strings.Join(connected, ","),
	)
}

// globalSearchTargets builds the UDP GlobSearch address list from server.met
// (ranked) plus configured lists. Connected servers are included as fillers so
// the UDP path always has up to `max` targets even when the TCP pool is full.
// skip drops the first N non-connected candidates so enrich passes fan out to
// a different slice of the met instead of re-hitting the same top ranks.
func (e *Engine) globalSearchTargets(max int, skip int) []string {
	if max <= 0 {
		max = 12
	}
	if skip < 0 {
		skip = 0
	}
	connected := e.connectedSet()
	seen := make(map[string]struct{}, max*2)
	out := make([]string, 0, max)
	skipped := 0
	appendUnique := func(addr string, allowConnected bool) bool {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			return false
		}
		if !allowConnected {
			if _, ok := connected[addr]; ok {
				return false
			}
			if skipped < skip {
				skipped++
				return false
			}
		}
		if _, ok := seen[addr]; ok {
			return false
		}
		seen[addr] = struct{}{}
		out = append(out, addr)
		return len(out) >= max
	}

	e.discMu.RLock()
	discovered := append([]string(nil), e.discovered...)
	e.discMu.RUnlock()

	pools := [][]string{discovered, e.app.AllServers, e.app.PrioritySlice()}
	for _, pool := range pools {
		for _, addr := range pool {
			if appendUnique(addr, false) {
				return out
			}
		}
	}
	for _, pool := range pools {
		for _, addr := range pool {
			if appendUnique(addr, true) {
				return out
			}
		}
	}
	return out
}

func sourceLabel(src ed2k.SearchResultSource) string {
	server := src&ed2k.SearchResultServer != 0
	kad := src&ed2k.SearchResultKAD != 0
	switch {
	case server && kad:
		return "both"
	case kad:
		return "kad"
	default:
		return "server"
	}
}

// toResult converts a core search hit into the API shape.
func toResult(r ed2k.SearchResult) Result {
	return Result{
		FileName:        r.FileName,
		FileSize:        r.FileSize,
		Hash:            r.Hash.String(),
		Sources:         r.Sources,
		CompleteSources: r.CompleteSources,
		Extension:       r.Extension,
		FileType:        r.FileType,
		ED2K:            r.ED2KLink(),
		Source:          sourceLabel(r.Source),
	}
}

func containsCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// resultKey identifies a hit for dedup; see ed2k.ResultKey.
func resultKey(r ed2k.SearchResult) string {
	return ed2k.ResultKey(r)
}

// searchPhase labels what the search is currently doing. The enrich flag wins
// because a second pass is a distinct, user-visible event; otherwise the
// session's busy flags say whether servers, KAD, or both are still answering.
func searchPhase(snap ed2k.SearchSnapshot, enrich bool) string {
	if enrich {
		return "enrich"
	}
	switch {
	case snap.ServerBusy && snap.GlobalBusy && snap.DHTBusy:
		return "local+global+kad"
	case snap.ServerBusy && snap.GlobalBusy:
		return "local+global"
	case snap.ServerBusy && snap.DHTBusy:
		return "local+kad"
	case snap.GlobalBusy && snap.DHTBusy:
		return "global+kad"
	case snap.ServerBusy:
		return "local"
	case snap.GlobalBusy:
		return "global"
	case snap.DHTBusy:
		return "kad"
	case snap.State == ed2k.SearchStateRunning:
		return "settling"
	default:
		return "searching"
	}
}

// progressEmitter turns polled snapshots into throttled SearchProgress frames.
//
// Two properties matter and are covered by tests:
//
//   - No result is ever dropped. A key is recorded as sent only when a frame
//     containing it is actually handed to emit, so a tick that gets coalesced
//     away simply re-sends the same delta next time.
//   - The client keeps seeing progress. When no new hit arrives, a bare
//     heartbeat frame still goes out every streamHeartbeatInterval so the UI
//     elapsed timer never looks frozen.
//
// A nil *progressEmitter is valid and inert, which is how the non-streaming
// Search path (emit == nil) avoids branching everywhere.
type progressEmitter struct {
	emit    func(SearchProgress) error
	query   string
	limit   int
	started time.Time

	sent     map[string]struct{}
	lastEmit time.Time
	err      error

	strategy string
	failover bool
	enrich   bool
}

func newProgressEmitter(emit func(SearchProgress) error, query string, limit int, started time.Time) *progressEmitter {
	if emit == nil {
		return nil
	}
	return &progressEmitter{
		emit:     emit,
		query:    query,
		limit:    limit,
		started:  started,
		sent:     make(map[string]struct{}),
		strategy: "local+global+kad",
	}
}

// enterEnrich switches the reported phase to the second (failover) pass.
func (p *progressEmitter) enterEnrich() {
	if p == nil {
		return
	}
	p.enrich = true
	p.strategy = "failover+backup"
	p.failover = true
}

// markNoServers records that the search started with no connected server, so
// every frame already reports the failover strategy.
func (p *progressEmitter) markNoServers() {
	if p == nil {
		return
	}
	p.strategy = "failover+backup"
	p.failover = true
}

func (p *progressEmitter) strategyLabel() (string, bool) {
	if p == nil {
		return "local+global+kad", false
	}
	return p.strategy, p.failover
}

func (p *progressEmitter) failed() bool {
	return p != nil && p.err != nil
}

func (p *progressEmitter) errValue() error {
	if p == nil {
		return nil
	}
	return p.err
}

// push forwards the hits the client has not seen yet in `cur`. `now` is
// injected so the throttling rules are testable without sleeping.
func (p *progressEmitter) push(cur ed2k.SearchSnapshot, now time.Time) {
	if p == nil || p.err != nil {
		return
	}
	since := now.Sub(p.lastEmit)

	var (
		delta []Result
		keys  []string
	)
	for _, r := range cur.Results {
		key := resultKey(r)
		if _, ok := p.sent[key]; ok {
			continue
		}
		keys = append(keys, key)
		delta = append(delta, toResult(r))
	}

	switch {
	case len(delta) > 0 && since >= streamEmitInterval:
	case len(delta) > 0:
		return // too soon since the last frame — coalesce into the next tick
	case since >= streamHeartbeatInterval:
		// No new hits, but keep the client's progress timer alive.
	default:
		return
	}

	for _, key := range keys {
		p.sent[key] = struct{}{}
	}
	p.lastEmit = now
	p.err = p.emit(SearchProgress{
		Query:     p.query,
		Phase:     searchPhase(cur, p.enrich),
		State:     string(cur.State),
		Delta:     delta,
		Count:     len(p.sent),
		Limit:     p.limit,
		ElapsedMs: now.Sub(p.started).Milliseconds(),
		Strategy:  p.strategy,
		Failover:  p.failover,
	})
}

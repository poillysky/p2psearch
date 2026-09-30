package engine

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	kadproto "github.com/goed2k/core/protocol/kad"
	serverproto "github.com/goed2k/core/protocol/server"
	"github.com/p2psearch/p2psearch/internal/proxyutil"
)

// ProbeKind classifies a connectivity check target.
type ProbeKind string

const (
	ProbeKindServer    ProbeKind = "server"
	ProbeKindServerMet ProbeKind = "server_met"
	ProbeKindNodesDat  ProbeKind = "nodes_dat"
)

// ProbeRequest lists sources to test (usually current form values).
type ProbeRequest struct {
	Servers       []string `json:"servers"`
	ServerMetURLs []string `json:"server_met_urls"`
	NodesDatURLs  []string `json:"nodes_dat_urls"`
}

// ProbeItem is one connectivity result.
type ProbeItem struct {
	Kind      ProbeKind `json:"kind"`
	Target    string    `json:"target"`
	OK        bool      `json:"ok"`
	LatencyMs int64     `json:"latency_ms"`
	Detail    string    `json:"detail,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// ProbeResponse aggregates all checks.
type ProbeResponse struct {
	Items []ProbeItem `json:"items"`
	Proxy string      `json:"proxy,omitempty"`
	Note  string      `json:"note"`
}

// ProbeSources tests eD2K servers (TCP) and HTTP bootstrap lists.
func (e *Engine) ProbeSources(ctx context.Context, req ProbeRequest) ProbeResponse {
	resp := ProbeResponse{
		Proxy: proxyutil.RedactString(e.app.ProxyURL),
		Note:  "server.met / nodes.dat 由第三方站点维护；本程序按 refresh_hours 自动重新下载并重连。此处只测连通与可解析性。",
	}
	var mu sync.Mutex
	var wg sync.WaitGroup

	add := func(item ProbeItem) {
		mu.Lock()
		resp.Items = append(resp.Items, item)
		mu.Unlock()
	}

	for _, addr := range cleanList(req.Servers) {
		wg.Add(1)
		go func(target string) {
			defer wg.Done()
			add(e.probeServerTCP(ctx, target))
		}(addr)
	}
	for _, u := range cleanList(req.ServerMetURLs) {
		wg.Add(1)
		go func(target string) {
			defer wg.Done()
			add(e.probeServerMet(ctx, target))
		}(u)
	}
	for _, u := range cleanList(req.NodesDatURLs) {
		wg.Add(1)
		go func(target string) {
			defer wg.Done()
			add(probeNodesDat(ctx, target))
		}(u)
	}
	wg.Wait()
	return resp
}

func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func (e *Engine) probeServerTCP(ctx context.Context, addr string) ProbeItem {
	item := ProbeItem{Kind: ProbeKindServer, Target: addr}
	started := time.Now()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		item.Error = "格式应为 host:port"
		return item
	}
	if host == "" || port == "" {
		item.Error = "格式应为 host:port"
		return item
	}

	dctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	var conn net.Conn
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	if proxyutil.SearchTunneled() {
		tcpAddr, rerr := net.ResolveTCPAddr("tcp", addr)
		if rerr != nil {
			item.Error = rerr.Error()
			item.LatencyMs = time.Since(started).Milliseconds()
			return item
		}
		conn, err = proxyutil.ED2KDialTCP(tcpAddr)
	} else {
		conn, err = dialer.DialContext(dctx, "tcp", addr)
	}
	item.LatencyMs = time.Since(started).Milliseconds()
	if err != nil {
		item.Error = err.Error()
		return item
	}
	_ = conn.Close()
	item.OK = true
	item.Detail = "TCP 可达"
	return item
}

func (e *Engine) probeServerMet(ctx context.Context, source string) ProbeItem {
	item := ProbeItem{Kind: ProbeKindServerMet, Target: source}
	started := time.Now()
	if e == nil || e.client == nil {
		item.Error = "引擎未就绪"
		return item
	}
	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		entries, err := e.client.LoadServerMet(source)
		ch <- result{n: len(entries), err: err}
	}()
	select {
	case <-ctx.Done():
		item.Error = ctx.Err().Error()
		item.LatencyMs = time.Since(started).Milliseconds()
		return item
	case <-time.After(20 * time.Second):
		item.Error = "超时"
		item.LatencyMs = time.Since(started).Milliseconds()
		return item
	case r := <-ch:
		item.LatencyMs = time.Since(started).Milliseconds()
		if r.err != nil {
			item.Error = r.err.Error()
			return item
		}
		item.OK = true
		item.Detail = fmt.Sprintf("可下载，解析到 %d 台服务器", r.n)
		return item
	}
}

func probeNodesDat(ctx context.Context, source string) ProbeItem {
	item := ProbeItem{Kind: ProbeKindNodesDat, Target: source}
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		item.Error = err.Error()
		return item
	}
	req.Header.Set("User-Agent", "p2psearch/1.0")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	item.LatencyMs = time.Since(started).Milliseconds()
	if err != nil {
		item.Error = err.Error()
		return item
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		item.Error = fmt.Sprintf("HTTP %s", resp.Status)
		return item
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		item.Error = err.Error()
		return item
	}
	nodes, err := kadproto.ParseNodesDat(data)
	if err != nil {
		// Still reachable as a download; report parse failure separately.
		item.OK = true
		item.Detail = fmt.Sprintf("可下载 %d 字节，但解析失败：%v", len(data), err)
		return item
	}
	item.OK = true
	item.Detail = fmt.Sprintf("可下载，解析到 %d 个 KAD 节点", len(nodes.Contacts))
	return item
}

// ServerMetEntryInfo is one ranked server.met row.
type ServerMetEntryInfo struct {
	Address string `json:"address"`
	Name    string `json:"name,omitempty"`
	Files   int64  `json:"files"`
	Users   int64  `json:"users"`
}

// ServerMetList is the parsed address list from one or more server.met sources.
type ServerMetList struct {
	Addresses []string             `json:"addresses"`
	Entries   []ServerMetEntryInfo `json:"entries"`
	Count     int                  `json:"count"`
	Sources   []ServerMetSource    `json:"sources"`
}

// ServerMetSource reports per-URL load result.
type ServerMetSource struct {
	URL     string `json:"url"`
	OK      bool   `json:"ok"`
	Entries int    `json:"entries,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ListServerMetAddresses downloads server.met entries, ranks by file count desc.
// limit <= 0 means return all unique entries.
func (e *Engine) ListServerMetAddresses(urls []string, limit int) ServerMetList {
	out := ServerMetList{
		Addresses: make([]string, 0, 64),
		Entries:   make([]ServerMetEntryInfo, 0, 64),
	}
	if e == nil || e.client == nil {
		return out
	}
	seen := map[string]struct{}{}
	ranked := make([]ServerMetEntryInfo, 0, 64)
	for _, src := range cleanList(urls) {
		srcInfo := ServerMetSource{URL: src}
		entries, err := e.client.LoadServerMet(src)
		if err != nil {
			srcInfo.Error = err.Error()
			out.Sources = append(out.Sources, srcInfo)
			continue
		}
		srcInfo.OK = true
		srcInfo.Entries = len(entries)
		out.Sources = append(out.Sources, srcInfo)
		for _, entry := range entries {
			addr := strings.TrimSpace(entry.Address())
			if addr == "" {
				continue
			}
			if _, ok := seen[addr]; ok {
				continue
			}
			seen[addr] = struct{}{}
			files, users := metFilesUsers(entry)
			ranked = append(ranked, ServerMetEntryInfo{
				Address: addr,
				Name:    entry.Name(),
				Files:   files,
				Users:   users,
			})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Files != ranked[j].Files {
			return ranked[i].Files > ranked[j].Files
		}
		if ranked[i].Users != ranked[j].Users {
			return ranked[i].Users > ranked[j].Users
		}
		return ranked[i].Address < ranked[j].Address
	})
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out.Entries = ranked
	for _, row := range ranked {
		out.Addresses = append(out.Addresses, row.Address)
	}
	out.Count = len(out.Addresses)
	return out
}

func metFilesUsers(entry serverproto.ServerMetEntry) (files, users int64) {
	for _, tag := range entry.Tags {
		switch strings.ToLower(strings.TrimSpace(tag.Name)) {
		case "files":
			files = int64(tag.UInt64)
		case "users":
			users = int64(tag.UInt64)
		}
	}
	return files, users
}

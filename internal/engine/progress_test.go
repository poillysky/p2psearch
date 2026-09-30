package engine

import (
	"errors"
	"testing"
	"time"

	ed2k "github.com/goed2k/core"
	"github.com/goed2k/core/protocol"
)

func mustHash(t *testing.T, s string) protocol.Hash {
	t.Helper()
	h, err := protocol.HashFromData([]byte(s))
	if err != nil {
		t.Fatalf("HashFromData(%q): %v", s, err)
	}
	return h
}

// snapWith builds a running snapshot holding one hit per name, each with a
// distinct hash and a sources count derived from the name length.
func snapWith(t *testing.T, names ...string) ed2k.SearchSnapshot {
	t.Helper()
	results := make([]ed2k.SearchResult, 0, len(names))
	for _, n := range names {
		results = append(results, ed2k.SearchResult{
			Hash:     mustHash(t, n),
			FileName: n,
			FileSize: int64(len(n) + 1),
			Sources:  len(n),
			Source:   ed2k.SearchResultServer,
		})
	}
	return ed2k.SearchSnapshot{State: ed2k.SearchStateRunning, Results: results}
}

func collector(limit int, start time.Time) (*progressEmitter, *[]SearchProgress) {
	frames := &[]SearchProgress{}
	p := newProgressEmitter(func(f SearchProgress) error {
		*frames = append(*frames, f)
		return nil
	}, "q", limit, start)
	return p, frames
}

func deltaNames(t *testing.T, f SearchProgress) []string {
	t.Helper()
	out := make([]string, 0, len(f.Delta))
	for _, r := range f.Delta {
		out = append(out, r.FileName)
	}
	return out
}

// A tick that arrives sooner than streamEmitInterval must be coalesced, not
// dropped: the results it carried have to show up in the next frame.
func TestProgressEmitterCoalescedTickLosesNothing(t *testing.T) {
	start := time.Now()
	p, frames := collector(0, start)

	p.push(snapWith(t, "alpha", "beta"), start)
	if len(*frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(*frames))
	}
	if got := len((*frames)[0].Delta); got != 2 {
		t.Fatalf("first delta = %d results, want 2", got)
	}

	// 10ms later one more hit shows up — below the emit interval, so it must
	// be coalesced away rather than marked as sent.
	p.push(snapWith(t, "alpha", "beta", "gamma"), start.Add(10*time.Millisecond))
	if len(*frames) != 1 {
		t.Fatalf("coalesced tick emitted a frame (frames = %d)", len(*frames))
	}

	// The next tick past the interval has to deliver the previously withheld
	// hit, and only that hit.
	p.push(snapWith(t, "alpha", "beta", "gamma"), start.Add(streamEmitInterval+time.Millisecond))
	if len(*frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(*frames))
	}
	last := (*frames)[1]
	if names := deltaNames(t, last); len(names) != 1 || names[0] != "gamma" {
		t.Fatalf("second delta = %v, want [gamma]", names)
	}
	if last.Count != 3 {
		t.Fatalf("running count = %d, want 3", last.Count)
	}
	if last.Query != "q" || last.Limit != 0 {
		t.Fatalf("frame metadata lost: query=%q limit=%d", last.Query, last.Limit)
	}
}

// With nothing new to report the emitter still has to keep the client's
// progress timer alive once the heartbeat window elapses.
func TestProgressEmitterHeartbeatsWithoutNewHits(t *testing.T) {
	start := time.Now()
	p, frames := collector(0, start)
	snap := snapWith(t, "alpha")

	p.push(snap, start)
	if len(*frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(*frames))
	}

	p.push(snap, start.Add(300*time.Millisecond))
	if len(*frames) != 1 {
		t.Fatalf("frames = %d, want 1 (300ms is below the heartbeat window)", len(*frames))
	}

	p.push(snap, start.Add(streamHeartbeatInterval+time.Millisecond))
	if len(*frames) != 2 {
		t.Fatalf("frames = %d, want 2 (heartbeat)", len(*frames))
	}
	hb := (*frames)[1]
	if len(hb.Delta) != 0 {
		t.Fatalf("heartbeat delta = %v, want empty", deltaNames(t, hb))
	}
	if hb.Count != 1 {
		t.Fatalf("heartbeat count = %d, want 1", hb.Count)
	}
	if want := (streamHeartbeatInterval + time.Millisecond).Milliseconds(); hb.ElapsedMs != want {
		t.Fatalf("heartbeat elapsed_ms = %d, want %d", hb.ElapsedMs, want)
	}
}

// Re-polling an unchanged snapshot must never resend hits or inflate the count.
func TestProgressEmitterNeverResendsSeenHits(t *testing.T) {
	start := time.Now()
	p, frames := collector(0, start)
	snap := snapWith(t, "a", "b", "c")

	p.push(snap, start)
	p.push(snap, start.Add(time.Second))
	p.push(snap, start.Add(2*time.Second))

	if len(*frames) != 3 {
		t.Fatalf("frames = %d, want 3", len(*frames))
	}
	for i, f := range *frames {
		if f.Count != 3 {
			t.Fatalf("frame %d count = %d, want 3", i, f.Count)
		}
	}
	for _, i := range []int{1, 2} {
		if names := deltaNames(t, (*frames)[i]); len(names) != 0 {
			t.Fatalf("frame %d resent %v", i, names)
		}
	}
}

// The enrich pass searches again with the same query; already-seen hits must
// not be re-sent, while the running count keeps accumulating across passes.
func TestProgressEmitterEnrichKeepsRunningUnion(t *testing.T) {
	start := time.Now()
	p, frames := collector(0, start)

	p.push(snapWith(t, "alpha"), start)
	p.enterEnrich()
	p.push(snapWith(t, "alpha", "delta"), start.Add(streamEmitInterval+time.Millisecond))

	if len(*frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(*frames))
	}
	last := (*frames)[1]
	if names := deltaNames(t, last); len(names) != 1 || names[0] != "delta" {
		t.Fatalf("enrich delta = %v, want [delta]", names)
	}
	if last.Count != 2 {
		t.Fatalf("enrich count = %d, want 2", last.Count)
	}
	if last.Phase != "enrich" {
		t.Fatalf("enrich phase = %q, want enrich", last.Phase)
	}
	if !last.Failover || last.Strategy != "failover+backup" {
		t.Fatalf("enrich strategy = %q failover=%v", last.Strategy, last.Failover)
	}
}

// The buffered Search path builds no emitter at all; a nil one must be inert
// rather than a source of panics or bogus metadata.
func TestProgressEmitterNilIsInert(t *testing.T) {
	var p *progressEmitter
	p.push(snapWith(t, "a"), time.Now())
	p.enterEnrich()
	p.markNoServers()

	if p.failed() {
		t.Fatal("nil emitter reported failure")
	}
	if err := p.errValue(); err != nil {
		t.Fatalf("errValue = %v, want nil", err)
	}
	if strategy, failover := p.strategyLabel(); strategy != "local+global+kad" || failover {
		t.Fatalf("strategyLabel = (%q, %v), want (local+global+kad, false)", strategy, failover)
	}
}

// Once the client hangs up the search must stop emitting and surface the error.
func TestProgressEmitterStopsAfterEmitError(t *testing.T) {
	start := time.Now()
	boom := errors.New("client gone")
	var got []SearchProgress
	p := newProgressEmitter(func(f SearchProgress) error {
		got = append(got, f)
		return boom
	}, "q", 0, start)

	p.push(snapWith(t, "a"), start)
	if !p.failed() {
		t.Fatal("failed() = false after emit error")
	}
	if !errors.Is(p.errValue(), boom) {
		t.Fatalf("errValue = %v, want %v", p.errValue(), boom)
	}

	p.push(snapWith(t, "a", "b"), start.Add(time.Second))
	if len(got) != 1 {
		t.Fatalf("emitted %d frames after failure, want 1", len(got))
	}
}

func TestSearchPhaseLabels(t *testing.T) {
	cases := []struct {
		name   string
		server bool
		global bool
		dht    bool
		state  ed2k.SearchState
		enrich bool
		want   string
	}{
		{"all three", true, true, true, ed2k.SearchStateRunning, false, "local+global+kad"},
		{"local+kad", true, false, true, ed2k.SearchStateRunning, false, "local+kad"},
		{"local+global", true, true, false, ed2k.SearchStateRunning, false, "local+global"},
		{"local only", true, false, false, ed2k.SearchStateRunning, false, "local"},
		{"global only", false, true, false, ed2k.SearchStateRunning, false, "global"},
		{"kad only", false, false, true, ed2k.SearchStateRunning, false, "kad"},
		{"draining", false, false, false, ed2k.SearchStateRunning, false, "settling"},
		{"idle", false, false, false, ed2k.SearchStateFinished, false, "searching"},
		{"enrich outranks busy flags", true, true, true, ed2k.SearchStateRunning, true, "enrich"},
		{"enrich outranks idle", false, false, false, ed2k.SearchStateFinished, true, "enrich"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := searchPhase(ed2k.SearchSnapshot{
				ServerBusy: c.server,
				GlobalBusy: c.global,
				DHTBusy:    c.dht,
				State:      c.state,
			}, c.enrich)
			if got != c.want {
				t.Fatalf("searchPhase = %q, want %q", got, c.want)
			}
		})
	}
}

// The warm-up must scale with the caller's wait but never dominate it, so a
// dead server pool cannot turn a 2s search into a 7s one.
func TestSearchWarmupBudgetIsBounded(t *testing.T) {
	cases := []struct {
		name string
		wait time.Duration
		want time.Duration
	}{
		{"zero wait falls back to the floor", 0, 300 * time.Millisecond},
		{"short wait gets a third", 2 * time.Second, 2 * time.Second / 3},
		{"long wait is capped", 6 * time.Second, 2 * time.Second},
		{"default wait is capped", 25 * time.Second, 2 * time.Second},
		{"five minute ceiling is capped", 5 * time.Minute, 2 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := searchWarmupBudget(c.wait); got != c.want {
				t.Fatalf("searchWarmupBudget(%v) = %v, want %v", c.wait, got, c.want)
			}
		})
	}

	// Whatever the wait, the warm-up has to stay a small slice of it.
	for _, wait := range []time.Duration{time.Second, 3 * time.Second, 25 * time.Second} {
		if got := searchWarmupBudget(wait); got > wait/2 {
			t.Fatalf("warm-up %v is more than half of wait %v", got, wait)
		}
	}
}

// A real hit is keyed by hash; an entry with no usable hash must still get a
// stable key of its own instead of colliding with every other such entry.
func TestResultKeyDistinguishesUnhashedHits(t *testing.T) {
	hashed := ed2k.SearchResult{Hash: mustHash(t, "x"), FileName: "x", FileSize: 7}
	if got, want := resultKey(hashed), hashed.Hash.String(); got != want {
		t.Fatalf("resultKey(hashed) = %q, want %q", got, want)
	}

	a := ed2k.SearchResult{FileName: "a", FileSize: 1}
	b := ed2k.SearchResult{FileName: "b", FileSize: 2}
	if ka, kb := resultKey(a), resultKey(b); ka == kb {
		t.Fatalf("unhashed hits collided on key %q", ka)
	}
	if got, want := resultKey(a), "a|1"; got != want {
		t.Fatalf("resultKey(unhashed) = %q, want %q", got, want)
	}
}

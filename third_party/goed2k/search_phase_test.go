package goed2k

import (
	"reflect"
	"testing"
)

func TestPickKadKeywords(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{"empty", "   ", nil},
		{"single", "ubuntu", []string{"ubuntu"}},
		{"longest first", "movie x264 1080p", []string{"movie", "1080p", "x264"}},
		{"drops short and dupes", "4k movie movie", []string{"movie"}},
		{"caps at three", "alpha bravo charlie delta", []string{"charlie", "alpha", "bravo"}},
		{"cjk tokens kept", "体检 报告", []string{"体检", "报告"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pickKadKeywords(tc.query)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("pickKadKeywords(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

func TestPickKadKeywordMatchesLongestToken(t *testing.T) {
	// The publish path files a resource under one keyword, which must stay the
	// longest token it used to pick.
	if got := pickKadKeyword("movie x264 1080p"); got != "movie" {
		t.Fatalf("pickKadKeyword = %q, want %q", got, "movie")
	}
}

func TestServerPhaseFinishesWhenAllServersReport(t *testing.T) {
	task := newSearchTask(1, SearchParams{}, CurrentTime())
	task.serverPhaseStarted(2, 15)
	if !task.snapshot().ServerBusy {
		t.Fatal("server phase should be busy after the fan-out")
	}

	task.noteServerBatch("a", false, 15)
	if got := task.snapshot().State; got != SearchStateRunning {
		t.Fatalf("one server still outstanding, state = %s", got)
	}

	task.noteServerBatch("b", false, 15)
	snap := task.snapshot()
	if snap.State != SearchStateFinished {
		t.Fatalf("all servers reported, state = %s, want %s", snap.State, SearchStateFinished)
	}
	if snap.ServerBusy {
		t.Fatal("ServerBusy should be false once every server reported")
	}
}

func TestServerPhaseStaysOpenUntilEveryServerReports(t *testing.T) {
	task := newSearchTask(2, SearchParams{}, CurrentTime())
	task.serverPhaseStarted(2, 15)
	task.noteServerBatch("a", false, 15) // one server retired
	task.noteServerBatch("b", true, 15)  // the other is still paging

	// Quiet gap must NOT end the phase while SearchMore is outstanding.
	task.onTick(CurrentTime() + 10_000)
	if !task.snapshot().ServerBusy {
		t.Fatal("phase ended while a server had not reported a terminal batch")
	}

	task.noteServerBatch("b", false, 15) // second server finished
	if snap := task.snapshot(); snap.ServerBusy || snap.State != SearchStateFinished {
		t.Fatalf("ServerBusy=%v state=%s, want finished after every server reported", snap.ServerBusy, snap.State)
	}
}

func TestServerPhaseQuietGapDropsSilentPeers(t *testing.T) {
	task := newSearchTask(5, SearchParams{}, CurrentTime())
	task.serverPhaseStarted(3, 15)
	task.noteServerBatch("a", false, 15) // one answered; two stay silent

	// Under 4s: still waiting for the silent peers.
	task.onTick(CurrentTime() + Seconds(3))
	if !task.snapshot().ServerBusy {
		t.Fatal("phase ended before the quiet-gap window")
	}

	// After 4s of silence with no SearchMore pending, abandon silent peers.
	task.onTick(CurrentTime() + Seconds(4) + 1)
	if task.snapshot().ServerBusy {
		t.Fatal("silent peers should not hold the server phase open")
	}
}

func TestServerPhaseHardCeilingEndsSilentPool(t *testing.T) {
	task := newSearchTask(3, SearchParams{}, CurrentTime())
	task.serverPhaseStarted(2, 15) // nobody ever answers

	task.onTick(CurrentTime() + Seconds(15) + 1)
	if got := task.snapshot().State; got != SearchStateFinished {
		t.Fatalf("state = %s, want %s at the safety ceiling", got, SearchStateFinished)
	}
}

func TestDHTPhaseWaitsForEveryKeyword(t *testing.T) {
	task := newSearchTask(4, SearchParams{}, CurrentTime())
	task.dhtPhaseStarted(3)

	task.dhtTraversalDone()
	task.dhtTraversalDone()
	if !task.snapshot().DHTBusy {
		t.Fatal("DHT phase closed while a traversal was still outstanding")
	}

	task.dhtTraversalDone()
	snap := task.snapshot()
	if snap.DHTBusy || snap.State != SearchStateFinished {
		t.Fatalf("state = %s dhtBusy = %v, want %s/false", snap.State, snap.DHTBusy, SearchStateFinished)
	}
}

func TestSearchFinishesOnlyWhenBothPhasesDone(t *testing.T) {
	task := newSearchTask(5, SearchParams{}, CurrentTime())
	task.serverPhaseStarted(1, 15)
	task.dhtPhaseStarted(1)

	task.noteServerBatch("a", false, 15)
	if got := task.snapshot().State; got != SearchStateRunning {
		t.Fatalf("KAD still running, state = %s", got)
	}

	task.dhtTraversalDone()
	if got := task.snapshot().State; got != SearchStateFinished {
		t.Fatalf("state = %s, want %s", got, SearchStateFinished)
	}
}

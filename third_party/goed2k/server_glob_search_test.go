package goed2k

import (
	"bytes"
	"testing"

	serverproto "github.com/goed2k/core/protocol/server"
)

func TestEncodeGlobSearchPayloadMatchesTCPTree(t *testing.T) {
	params := SearchParams{Query: "体检", Charset: "utf8"}
	udp, err := encodeGlobSearchPayload(params)
	if err != nil {
		t.Fatal(err)
	}
	tcp := serverproto.SearchRequest{Query: "体检", Charset: "utf8"}
	var buf bytes.Buffer
	if err := tcp.Put(&buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(udp, buf.Bytes()) {
		t.Fatalf("UDP payload must equal TCP search tree\nudp=%x\ntcp=%x", udp, buf.Bytes())
	}
}

func TestParseGlobSearchUDPSingleAndChained(t *testing.T) {
	entry := serverproto.SharedFileEntry{}
	// Minimal valid-looking entry is hard without tags; build via Put of empty-ish
	// entry then wrap with opcode headers.
	var body bytes.Buffer
	if err := entry.Put(&body); err != nil {
		t.Fatal(err)
	}
	frame := append([]byte{ed2kUDPHeader, opGlobSearchRes}, body.Bytes()...)
	chained := append(append([]byte{}, frame...), frame...)

	got := parseGlobSearchUDP(frame)
	if len(got) != 1 {
		t.Fatalf("single frame entries = %d, want 1", len(got))
	}
	got = parseGlobSearchUDP(chained)
	if len(got) != 2 {
		t.Fatalf("chained frame entries = %d, want 2", len(got))
	}
}

func TestGlobalPhaseKeepsSearchRunning(t *testing.T) {
	task := newSearchTask(1, SearchParams{}, CurrentTime())
	task.globalPhaseStarted(3, 15)
	snap := task.snapshot()
	if !snap.GlobalBusy || snap.State != SearchStateRunning {
		t.Fatalf("GlobalBusy=%v state=%s", snap.GlobalBusy, snap.State)
	}
	// Server+DHT idle must not finish while global is collecting.
	task.mu.Lock()
	task.finishLocked()
	task.mu.Unlock()
	if task.snapshot().State != SearchStateRunning {
		t.Fatal("search finished too early while global busy")
	}
}

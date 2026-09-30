package goed2k

import (
	"bytes"
	"net"
	"strings"
	"time"

	serverproto "github.com/goed2k/core/protocol/server"
	"github.com/goed2k/core/internal/logx"
)

// eMule client↔server UDP search opcodes (opcodes.h).
const (
	opGlobSearchReq  byte = 0x98 // classic GlobSearch; payload = TCP search tree
	opGlobSearchReq2 byte = 0x92 // extended (requires UDP flags)
	opGlobSearchRes  byte = 0x99
)

const (
	defaultGlobalSearchMax     = 12
	defaultGlobalSearchTimeout = 12 // seconds
	globalUDPBatchSize         = 4
	globalUDPBatchGap          = 200 * time.Millisecond
)

// startGlobalUDPSearch fans OP_GLOBSEARCHREQ out to many servers over UDP
// (TCP port + 4). This is eMule/aMule "global search": covers servers we are
// not TCP-connected to, without holding those TCP sessions.
func (s *Session) startGlobalUDPSearch(task *searchTask, params SearchParams) bool {
	conn := s.globUDPWriteConn()
	if conn == nil {
		_ = s.EnsureServerStatUDPListener()
		conn = s.globUDPWriteConn()
	}
	if conn == nil {
		return false
	}

	targets := s.resolveGlobalUDPTargets(params)
	if len(targets) == 0 {
		return false
	}

	payloads := make([][]byte, 0, 2)
	primary, err := encodeGlobSearchPayload(params)
	if err != nil || len(primary) == 0 {
		return false
	}
	payloads = append(payloads, primary)
	if serverproto.ContainsCJK(params.Query) {
		alt := params
		if strings.EqualFold(params.Charset, "gbk") {
			alt.Charset = "utf8"
		} else {
			alt.Charset = "gbk"
		}
		if other, err := encodeGlobSearchPayload(alt); err == nil && len(other) > 0 && !bytes.Equal(other, primary) {
			payloads = append(payloads, other)
		}
	}

	timeout := s.settings.ServerSearchTimeout
	if timeout <= 0 {
		timeout = defaultGlobalSearchTimeout
	}
	task.globalPhaseStarted(len(targets)*len(payloads), timeout)

	go s.dispatchGlobalUDPSearch(conn, targets, payloads)
	logx.Debug("global UDP search dispatched",
		"targets", len(targets),
		"payloads", len(payloads),
		"query", params.Query,
	)
	return true
}

func (s *Session) resolveGlobalUDPTargets(params SearchParams) []*net.UDPAddr {
	max := params.GlobalMax
	if max <= 0 {
		max = defaultGlobalSearchMax
	}

	connected := make(map[string]struct{})
	for _, sc := range s.activeServerConnections() {
		if sc == nil || !sc.IsHandshakeCompleted() {
			continue
		}
		if addr := sc.GetAddress(); addr != nil {
			connected[addr.String()] = struct{}{}
			connected[sc.GetIdentifier()] = struct{}{}
		}
	}

	seen := make(map[string]struct{}, max)
	out := make([]*net.UDPAddr, 0, max)
	appendAddr := func(hostPort string, allowConnected bool) bool {
		hostPort = strings.TrimSpace(hostPort)
		if hostPort == "" {
			return false
		}
		if !allowConnected {
			if _, ok := connected[hostPort]; ok {
				return false
			}
		}
		tcpAddr, err := net.ResolveTCPAddr("tcp", hostPort)
		if err != nil || tcpAddr == nil || tcpAddr.IP == nil {
			return false
		}
		key := tcpAddr.String()
		if !allowConnected {
			if _, ok := connected[key]; ok {
				return false
			}
		}
		if _, ok := seen[key]; ok {
			return false
		}
		seen[key] = struct{}{}
		out = append(out, &net.UDPAddr{IP: tcpAddr.IP, Port: tcpAddr.Port + serverUDPPortOffset})
		return len(out) >= max
	}

	candidates := append([]string(nil), params.GlobalServers...)
	s.mu.Lock()
	for id := range s.configuredServers {
		candidates = append(candidates, id)
	}
	s.mu.Unlock()

	// Prefer servers we are not TCP-connected to (true global coverage).
	for _, a := range candidates {
		if appendAddr(a, false) {
			return out
		}
	}
	// Fill remaining slots with connected servers so UDP path still runs when
	// the TCP pool already absorbed the top of server.met.
	for _, a := range candidates {
		if appendAddr(a, true) {
			break
		}
	}
	return out
}

func encodeGlobSearchPayload(params SearchParams) ([]byte, error) {
	req := serverproto.SearchRequest{
		Query:              params.Query,
		Charset:            params.Charset,
		MinSize:            params.MinSize,
		MaxSize:            params.MaxSize,
		MinSources:         params.MinSources,
		MinCompleteSources: params.MinCompleteSources,
		FileType:           params.FileType,
		Extension:          params.Extension,
	}
	var buf bytes.Buffer
	if err := req.Put(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *Session) dispatchGlobalUDPSearch(conn *net.UDPConn, targets []*net.UDPAddr, payloads [][]byte) {
	packets := make([][]byte, 0, len(payloads))
	for _, payload := range payloads {
		pkt := make([]byte, 2+len(payload))
		pkt[0], pkt[1] = ed2kUDPHeader, opGlobSearchReq
		copy(pkt[2:], payload)
		packets = append(packets, pkt)
	}
	for i := 0; i < len(targets); i += globalUDPBatchSize {
		end := i + globalUDPBatchSize
		if end > len(targets) {
			end = len(targets)
		}
		for _, addr := range targets[i:end] {
			if addr == nil {
				continue
			}
			for _, pkt := range packets {
				_, _ = conn.WriteToUDP(pkt, addr)
			}
		}
		if end < len(targets) {
			time.Sleep(globalUDPBatchGap)
		}
	}
}

// handleGlobSearchUDP processes OP_GLOBSEARCHRES (and chained copies in one datagram).
func (s *Session) handleGlobSearchUDP(addr *net.UDPAddr, buf []byte) {
	if len(buf) < 3 || buf[0] != ed2kUDPHeader {
		return
	}
	entries := parseGlobSearchUDP(buf)
	if len(entries) == 0 {
		return
	}

	s.searchMu.Lock()
	task := s.activeSearch
	s.searchMu.Unlock()
	if task == nil || task.state != SearchStateRunning {
		return
	}
	params := task.params
	hit := false
	for _, entry := range entries {
		result := makeSearchResultFromServer(entry)
		result.Source = SearchResultServer
		if !matchesSearchFilters(result, params) {
			continue
		}
		task.mergeResult(result)
		hit = true
	}
	if hit {
		task.noteGlobalHit()
	}
	_ = addr
}

// parseGlobSearchUDP extracts SharedFileEntry values from one or more
// OP_GLOBSEARCHRES frames in a datagram. Per the eD2K UDP spec the result list
// has no count prefix; aMule also chains multiple (0xe3,0x99,entry) frames.
func parseGlobSearchUDP(buf []byte) []serverproto.SharedFileEntry {
	var out []serverproto.SharedFileEntry
	off := 0
	for off+2 <= len(buf) {
		if buf[off] != ed2kUDPHeader || buf[off+1] != opGlobSearchRes {
			break
		}
		off += 2
		consumed := 0
		for off+consumed < len(buf) {
			remain := buf[off+consumed:]
			if len(remain) >= 2 && remain[0] == ed2kUDPHeader && remain[1] == opGlobSearchRes && consumed > 0 {
				break
			}
			r := bytes.NewReader(remain)
			var e serverproto.SharedFileEntry
			if err := e.Get(r); err != nil {
				break
			}
			out = append(out, e)
			consumed += len(remain) - r.Len()
			if r.Len() == 0 {
				break
			}
		}
		if consumed == 0 {
			break
		}
		off += consumed
	}
	return out
}

// handleServerUDP dispatches all client↔server UDP opcodes on the shared socket.
func (s *Session) handleServerUDP(addr *net.UDPAddr, buf []byte) {
	if len(buf) < 2 || buf[0] != ed2kUDPHeader {
		return
	}
	switch buf[1] {
	case opGlobServStatRes:
		s.handleGlobServStatUDP(addr, buf)
	case opGlobSearchRes:
		s.handleGlobSearchUDP(addr, buf)
	}
}

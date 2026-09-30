// Per-server e2e: direct vs proxy TCP, handshake, and search hit check.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	ed2k "github.com/goed2k/core"
	"github.com/p2psearch/p2psearch/internal/proxyutil"
)

type modeResult struct {
	Mode      string `json:"mode"` // direct | proxy
	TCPOK     bool   `json:"tcp_ok"`
	TCPMs     int64  `json:"tcp_ms"`
	TCPErr    string `json:"tcp_err,omitempty"`
	Handshake bool   `json:"handshake"`
	HSMs      int64  `json:"handshake_ms,omitempty"`
	HSErr     string `json:"handshake_err,omitempty"`
	Hits      int    `json:"hits"`
	Sample    string `json:"sample,omitempty"`
	SearchErr string `json:"search_err,omitempty"`
}

type serverReport struct {
	Address string       `json:"address"`
	Direct  modeResult   `json:"direct"`
	Proxy   *modeResult  `json:"proxy,omitempty"`
	Verdict string       `json:"verdict"`
}

func main() {
	query := "pdf"
	if len(os.Args) > 1 {
		query = os.Args[1]
	}
	proxyURL := strings.TrimSpace(os.Getenv("E2E_PROXY"))
	if proxyURL == "" {
		proxyURL = "http://127.0.0.1:7897"
	}
	servers := []string{
		"176.123.5.89:4725",
		"77.42.68.79:4232",
		"85.17.116.222:6082",
		"91.208.162.87:4232",
		"85.121.5.137:4232",
		"212.95.35.240:4232",
		"213.141.198.207:4232",
		"57.131.35.107:4232",
		"212.95.35.240:4323",
		"91.208.162.55:4235",
		"193.187.90.12:4661",
		"91.126.170.253:5687",
	}
	if one := strings.TrimSpace(os.Getenv("E2E_SERVER")); one != "" {
		servers = []string{one}
	}

	hsOnly := strings.EqualFold(os.Getenv("E2E_HANDSHAKE_ONLY"), "1") ||
		strings.EqualFold(os.Getenv("E2E_HANDSHAKE_ONLY"), "true")
	directOnly := strings.EqualFold(os.Getenv("E2E_DIRECT_ONLY"), "1") ||
		strings.EqualFold(os.Getenv("E2E_DIRECT_ONLY"), "true")

	proxyOK := false
	if !directOnly {
		if _, err := proxyutil.Configure(proxyURL); err != nil {
			fmt.Fprintf(os.Stderr, "proxy configure failed (%s): %v — proxy column skipped\n", proxyURL, err)
			_, _ = proxyutil.Configure("")
		} else {
			proxyOK = true
		}
	}
	fmt.Fprintf(os.Stderr, "proxy=%v query=%q servers=%d handshake_only=%v\n", proxyOK, query, len(servers), hsOnly)

	reports := make([]serverReport, 0, len(servers))
	basePort := 4900
	for i, addr := range servers {
		fmt.Fprintf(os.Stderr, "[%d/%d] %s\n", i+1, len(servers), addr)
		rep := serverReport{Address: addr}
		rep.Direct = runMode(addr, query, "direct", basePort+i*4, false, hsOnly)
		if proxyOK {
			pr := runMode(addr, query, "proxy", basePort+i*4+2, true, hsOnly)
			rep.Proxy = &pr
		}
		rep.Verdict = decide(rep)
		fmt.Fprintf(os.Stderr, "  -> %s\n", rep.Verdict)
		reports = append(reports, rep)
	}

	_, _ = proxyutil.Configure("") // restore

	printTable(reports, proxyOK)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{
		"query":   query,
		"proxy":   proxyURL,
		"reports": reports,
	})
}

func decide(r serverReport) string {
	dConn := r.Direct.Handshake
	pConn := r.Proxy != nil && r.Proxy.Handshake
	dOK := dConn && r.Direct.Hits > 0
	pOK := pConn && r.Proxy != nil && r.Proxy.Hits > 0
	// Handshake-only runs set SearchErr="skipped" (or never searched).
	hsOnly := r.Direct.SearchErr == "skipped" ||
		(r.Proxy != nil && r.Proxy.SearchErr == "skipped") ||
		(r.Direct.Hits == 0 && r.Direct.SearchErr != "" && strings.Contains(r.Direct.SearchErr, "handshake"))

	if r.Direct.SearchErr == "skipped" || (r.Proxy != nil && r.Proxy.SearchErr == "skipped") || hsOnly && r.Direct.Hits == 0 && (r.Proxy == nil || r.Proxy.Hits == 0) {
		switch {
		case dConn && (r.Proxy == nil || pConn):
			return "握手成功"
		case dConn && r.Proxy != nil && !pConn:
			return "直连握手成功；代理握手失败 — 用直连"
		case !dConn && pConn:
			return "直连握手失败；代理握手成功 — 需要代理"
		default:
			return "握手失败"
		}
	}

	switch {
	case dOK:
		if r.Proxy != nil && !pConn {
			return "直连可用（搜到资源）；代理不可达 — 无需代理"
		}
		return "直连可用（搜到资源）— 无需代理"
	case dConn && r.Direct.Hits == 0:
		if pOK {
			return "直连能握手但无结果；代理能搜到 — 建议走代理搜"
		}
		return "直连能握手但无结果（该服可能无此关键词）"
	case !dConn && pOK:
		return "直连失败；代理可用 — 需要代理"
	case !dConn && pConn && !pOK:
		return "仅代理能握手，但仍无结果"
	case !dConn && (r.Proxy == nil || !pConn):
		return "直连与代理均失败 — 服务器宕机或被墙"
	default:
		return "异常状态"
	}
}

func runMode(addr, query, mode string, listenPort int, useProxy, hsOnly bool) modeResult {
	out := modeResult{Mode: mode}

	// TCP dial
	t0 := time.Now()
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		out.TCPErr = err.Error()
		return out
	}
	var conn net.Conn
	if useProxy {
		conn, err = proxyutil.ED2KDialTCP(tcpAddr)
	} else {
		d := net.Dialer{Timeout: 8 * time.Second}
		conn, err = d.DialContext(context.Background(), "tcp", tcpAddr.String())
	}
	out.TCPMs = time.Since(t0).Milliseconds()
	if err != nil {
		out.TCPErr = err.Error()
		return out
	}
	_ = conn.Close()
	out.TCPOK = true

	// Full ed2k client: handshake + search
	settings := ed2k.NewSettings()
	settings.ListenPort = listenPort
	settings.UDPPort = listenPort + 1
	settings.EnableDHT = false
	settings.EnableUPnP = false
	settings.ReconnectToServer = false
	settings.EnableCryptLayer = true
	settings.ClientName = "eMule"
	settings.ModName = "eMule"
	settings.ServerSearchTimeout = 12
	settings.ServerPingTimeout = 20

	savedDial := ed2k.DialTCP
	defer func() { ed2k.DialTCP = savedDial }()

	if useProxy {
		ed2k.DialTCP = proxyutil.ED2KDialTCP
	} else {
		ed2k.DialTCP = func(addr *net.TCPAddr) (net.Conn, error) {
			return net.DialTCP("tcp", nil, addr)
		}
	}

	client := ed2k.NewClient(settings)
	if err := client.Start(); err != nil {
		out.HSErr = "start: " + err.Error()
		return out
	}
	defer client.Close()

	t1 := time.Now()
	if err := client.Connect(addr); err != nil {
		out.HSErr = "connect: " + err.Error()
		return out
	}
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if len(client.Session().ConnectedServerIDs()) > 0 {
			out.Handshake = true
			out.HSMs = time.Since(t1).Milliseconds()
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !out.Handshake {
		out.HSErr = "handshake timeout"
		return out
	}
	if hsOnly {
		out.SearchErr = "skipped"
		return out
	}
	time.Sleep(400 * time.Millisecond)

	handle, err := client.StartSearch(ed2k.SearchParams{
		Query:   query,
		Scope:   ed2k.SearchScopeServer,
		Charset: "utf8",
	})
	if err != nil {
		out.SearchErr = err.Error()
		return out
	}
	defer handle.Stop()

	end := time.Now().Add(12 * time.Second)
	var snap ed2k.SearchSnapshot
	for time.Now().Before(end) {
		snap = handle.Snapshot()
		if snap.State == ed2k.SearchStateFinished ||
			snap.State == ed2k.SearchStateFailed ||
			snap.State == ed2k.SearchStateStopped {
			break
		}
		if len(snap.Results) > 0 {
			// got hits; can stop early for e2e
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	snap = handle.Snapshot()
	out.Hits = len(snap.Results)
	for _, r := range snap.Results {
		if r.FileName != "" {
			out.Sample = truncate(r.FileName, 40)
			break
		}
	}
	if snap.State == ed2k.SearchStateFailed && out.Hits == 0 && snap.Error != "" {
		out.SearchErr = snap.Error
	}
	return out
}

func printTable(reports []serverReport, withProxy bool) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "===== e2e summary =====")
	if withProxy {
		fmt.Fprintf(os.Stderr, "%-22s %8s %5s %5s  %8s %5s %5s  %s\n",
			"server", "dirTCP", "dirHS", "hits", "pxTCP", "pxHS", "hits", "verdict")
	} else {
		fmt.Fprintf(os.Stderr, "%-22s %8s %5s %5s  %s\n",
			"server", "dirTCP", "dirHS", "hits", "verdict")
	}
	for _, r := range reports {
		dTCP := yn(r.Direct.TCPOK)
		if r.Direct.TCPOK {
			dTCP = fmt.Sprintf("%dms", r.Direct.TCPMs)
		}
		dHS := yn(r.Direct.Handshake)
		if withProxy && r.Proxy != nil {
			pTCP := yn(r.Proxy.TCPOK)
			if r.Proxy.TCPOK {
				pTCP = fmt.Sprintf("%dms", r.Proxy.TCPMs)
			}
			pHS := yn(r.Proxy.Handshake)
			fmt.Fprintf(os.Stderr, "%-22s %8s %5s %5d  %8s %5s %5d  %s\n",
				r.Address, dTCP, dHS, r.Direct.Hits, pTCP, pHS, r.Proxy.Hits, r.Verdict)
		} else {
			fmt.Fprintf(os.Stderr, "%-22s %8s %5s %5d  %s\n",
				r.Address, dTCP, dHS, r.Direct.Hits, r.Verdict)
		}
	}
	fmt.Fprintln(os.Stderr)
}

func yn(v bool) string {
	if v {
		return "OK"
	}
	return "FAIL"
}

func truncate(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}

package proxyutil

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

var (
	ed2kNetworkDial func(network, address string) (net.Conn, error)
	baseTransport   = func() *http.Transport {
		if t, ok := http.DefaultTransport.(*http.Transport); ok {
			return t.Clone()
		}
		return &http.Transport{}
	}()
)

func clearProxyEnv() {
	for _, k := range []string{
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY",
		"http_proxy", "https_proxy", "all_proxy",
	} {
		_ = os.Unsetenv(k)
	}
}

// Configure applies an outbound proxy for HTTP(S) bootstrap downloads
// (server.met / nodes.dat) and, when possible, eD2K server TCP (search).
// Supports:
//   - http://host:port   (CONNECT tunnel for eD2K TCP)
//   - https://host:port
//   - socks5://host:port
//   - socks5h://host:port
// Empty raw clears any previously applied proxy (hot reload safe).
func Configure(raw string) (string, error) {
	ed2kNetworkDial = nil
	clearProxyEnv()
	http.DefaultTransport = baseTransport.Clone()

	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse proxy url: %w", err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("proxy url missing host")
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https":
		if err := applyHTTPProxy(u); err != nil {
			return "", err
		}
	case "socks5", "socks5h":
		if err := applySOCKS5(u); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("unsupported proxy scheme %q (use http/https/socks5)", u.Scheme)
	}
	return Redact(u), nil
}

// SearchTunneled reports whether eD2K TCP (server search) is proxied.
func SearchTunneled() bool {
	return ed2kNetworkDial != nil
}

// ED2KDialTCP is assigned to goed2k.DialTCP so server connections can tunnel.
func ED2KDialTCP(addr *net.TCPAddr) (net.Conn, error) {
	if addr == nil {
		return nil, fmt.Errorf("nil tcp addr")
	}
	if ed2kNetworkDial == nil {
		return net.DialTCP("tcp", nil, addr)
	}
	return ed2kNetworkDial("tcp", addr.String())
}

func applyHTTPProxy(u *url.URL) error {
	_ = os.Setenv("HTTP_PROXY", u.String())
	_ = os.Setenv("HTTPS_PROXY", u.String())
	_ = os.Setenv("http_proxy", u.String())
	_ = os.Setenv("https_proxy", u.String())

	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = http.ProxyURL(u)
	http.DefaultTransport = base

	ed2kNetworkDial = func(network, address string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil, fmt.Errorf("proxy: unsupported network %s", network)
		}
		return dialHTTPConnect(u, address)
	}
	return nil
}

func applySOCKS5(u *url.URL) error {
	_ = os.Setenv("ALL_PROXY", u.String())
	_ = os.Setenv("all_proxy", u.String())

	dialer, err := proxy.FromURL(u, proxy.Direct)
	if err != nil {
		return fmt.Errorf("socks5 dialer: %w", err)
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		contextDialer = &legacyContextDialer{Dialer: dialer}
	}

	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return contextDialer.DialContext(ctx, network, addr)
	}
	http.DefaultTransport = base

	ed2kNetworkDial = func(network, address string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return contextDialer.DialContext(ctx, network, address)
	}
	return nil
}

func dialHTTPConnect(proxyURL *url.URL, dest string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", proxyURL.Host, 15*time.Second)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", dest, dest)
	if proxyURL.User != nil {
		user := proxyURL.User.Username()
		pass, _ := proxyURL.User.Password()
		token := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
		fmt.Fprintf(&b, "Proxy-Authorization: Basic %s\r\n", token)
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("proxy CONNECT %s: %s", dest, resp.Status)
	}
	return conn, nil
}

type legacyContextDialer struct {
	proxy.Dialer
}

func (d *legacyContextDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	type result struct {
		c net.Conn
		e error
	}
	ch := make(chan result, 1)
	go func() {
		c, e := d.Dial(network, addr)
		ch <- result{c, e}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		return r.c, r.e
	case <-time.After(30 * time.Second):
		return nil, context.DeadlineExceeded
	}
}

// Redact hides userinfo in proxy URLs for logs/status.
func Redact(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	if c.User != nil {
		if _, has := c.User.Password(); has {
			c.User = url.UserPassword(c.User.Username(), "***")
		}
	}
	return c.String()
}

// RedactString parses and redacts a proxy URL string.
func RedactString(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid)"
	}
	return Redact(u)
}

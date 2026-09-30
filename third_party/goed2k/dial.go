package goed2k

import "net"

// DialTCP dials an eD2K TCP endpoint. Host apps may replace this to tunnel
// server connections through SOCKS5 or HTTP CONNECT.
var DialTCP = func(addr *net.TCPAddr) (net.Conn, error) {
	return net.DialTCP("tcp", nil, addr)
}

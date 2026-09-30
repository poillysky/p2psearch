// Quick probe: what happens after TCP to a single ed2k server.
package main

import (
	"fmt"
	"net"
	"os"
	"time"

	ed2k "github.com/goed2k/core"
)

func main() {
	addr := "91.208.162.55:4235"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	settings := ed2k.NewSettings()
	settings.ListenPort = 4991
	settings.UDPPort = 4992
	settings.EnableDHT = false
	settings.EnableUPnP = false
	settings.ReconnectToServer = false
	settings.EnableCryptLayer = true
	settings.ClientName = "eMule"
	settings.ModName = "eMule"
	settings.ServerPingTimeout = 30
	client := ed2k.NewClient(settings)
	if err := client.Start(); err != nil {
		fmt.Println("start:", err)
		os.Exit(1)
	}
	defer client.Close()

	t0 := time.Now()
	if err := client.Connect(addr); err != nil {
		fmt.Println("connect err:", err, "after", time.Since(t0))
		os.Exit(1)
	}
	fmt.Println("dial+login queued", time.Since(t0))

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		ids := client.Session().ConnectedServerIDs()
		snaps := client.ServerStatuses()
		for _, s := range snaps {
			if s.Identifier == addr || (s.Address != "" && s.Address == mustResolve(addr)) {
				fmt.Printf("t=%4dms id=%s addr=%s hs=%v connected=%v disconnecting=%v name=%q\n",
					time.Since(t0).Milliseconds(), s.Identifier, s.Address,
					s.HandshakeCompleted, s.Connected, s.Disconnecting, s.Name)
				if s.HandshakeCompleted {
					fmt.Println("SUCCESS")
					return
				}
			}
		}
		if len(ids) > 0 {
			fmt.Println("ConnectedServerIDs", ids, time.Since(t0))
			fmt.Println("SUCCESS via IDs")
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Println("TIMEOUT — no IdChange in 20s")
	for _, s := range client.ServerStatuses() {
		fmt.Printf("final: id=%s addr=%s hs=%v connected=%v disc=%v\n",
			s.Identifier, s.Address, s.HandshakeCompleted, s.Connected, s.Disconnecting)
	}
}

func mustResolve(addr string) string {
	a, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return addr
	}
	return a.String()
}

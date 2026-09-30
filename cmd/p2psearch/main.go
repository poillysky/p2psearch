package main

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	ed2k "github.com/goed2k/core"
	"github.com/p2psearch/p2psearch/internal/api"
	"github.com/p2psearch/p2psearch/internal/config"
	"github.com/p2psearch/p2psearch/internal/engine"
	"github.com/p2psearch/p2psearch/internal/proxyutil"
	"github.com/p2psearch/p2psearch/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	app, err := config.Load(os.Getenv("CONFIG_PATH"))
	if err != nil {
		log.Error("load config failed", "err", err)
		os.Exit(1)
	}

	if redacted, err := proxyutil.Configure(app.ProxyURL); err != nil {
		log.Error("configure proxy failed", "err", err)
		os.Exit(1)
	} else {
		ed2k.DialTCP = proxyutil.ED2KDialTCP
		if redacted != "" {
			log.Info("outbound proxy enabled", "proxy", redacted,
				"search_via_proxy", proxyutil.SearchTunneled())
		}
	}

	log.Info("starting ed2k engine",
		"http", app.HTTPAddr,
		"listen_port", app.ListenPort,
		"udp_port", app.UDPPort,
		"kad", app.EnableKAD,
		"upnp", app.EnableUPnP,
		"priority_servers", app.MaxPriorityServers,
		"max_total_servers", app.MaxTotalServers,
		"refresh", app.RefreshInterval.String(),
		"proxy", proxyutil.RedactString(app.ProxyURL),
	)

	eng, err := engine.Start(&app, log)
	if err != nil {
		log.Error("engine start failed", "err", err)
		os.Exit(1)
	}
	defer eng.Close()

	var webFS fs.FS = web.FS
	srv := &api.Server{Engine: eng, App: &app, WebFS: webFS}
	httpServer := &http.Server{
		Addr:              app.HTTPAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("http listening", "addr", app.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server error", "err", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}

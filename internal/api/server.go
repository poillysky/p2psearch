package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/p2psearch/p2psearch/internal/cloud115"
	"github.com/p2psearch/p2psearch/internal/config"
	"github.com/p2psearch/p2psearch/internal/engine"
)

// Server serves the search API and static web UI.
type Server struct {
	Engine *engine.Engine
	App    *config.App
	WebFS  fs.FS

	mu sync.RWMutex
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/search/stream", s.handleSearchStream)
	mux.HandleFunc("GET /search", s.handleSearch)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("POST /api/config", s.handlePutConfig)
	mux.HandleFunc("POST /api/probe", s.handleProbe)
	mux.HandleFunc("GET /api/probe", s.handleProbe)
	mux.HandleFunc("POST /api/server-met", s.handleServerMet)
	mux.HandleFunc("GET /api/server-met", s.handleServerMet)
	mux.HandleFunc("GET /api/115/status", s.handle115Status)
	mux.HandleFunc("GET /api/115/folders", s.handle115Folders)
	mux.HandleFunc("POST /api/115/folders", s.handle115Folders)
	mux.HandleFunc("POST /api/115/offline", s.handle115Offline)

	if s.WebFS != nil {
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, s.WebFS, "index.html")
		})
		mux.HandleFunc("GET /settings", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, s.WebFS, "settings.html")
		})
		mux.HandleFunc("GET /settings.html", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, s.WebFS, "settings.html")
		})
		mux.HandleFunc("GET /app.js", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, s.WebFS, "app.js")
		})
		mux.HandleFunc("GET /settings.js", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, s.WebFS, "settings.js")
		})
		mux.HandleFunc("GET /style.css", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, s.WebFS, "style.css")
		})
	}
	return withCORS(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"ready":   s.Engine != nil && s.Engine.Status().Ready,
		"service": "p2psearch",
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if s.Engine == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "engine not started"})
		return
	}
	writeJSON(w, http.StatusOK, s.Engine.Status())
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.App == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "config unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, s.App.Public())
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.App == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "config unavailable"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body failed"})
		return
	}
	var req config.UpdateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	before := *s.App
	s.App.ApplyUpdate(req)
	if err := s.App.Save(); err != nil {
		*s.App = before
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	applied, needRestart := s.Engine.ApplyLive(before)
	view := s.App.Public()
	view.RestartRequired = len(needRestart) > 0
	view.Applied = applied
	view.NeedRestart = needRestart
	if view.RestartRequired {
		view.Note = "已保存并热更新部分设置；以下项仍需重启进程：" + strings.Join(needRestart, "、")
	} else if len(applied) > 0 {
		view.Note = "已保存并立即生效：" + strings.Join(applied, "、")
	} else {
		view.Note = "已保存。"
	}
	writeJSON(w, http.StatusOK, view)
}

// parseSearchQuery reads the parameters shared by the buffered and streaming
// search endpoints.
func (s *Server) parseSearchQuery(r *http.Request) (query string, wait time.Duration, limit int) {
	query = r.URL.Query().Get("q")
	if query == "" {
		query = r.URL.Query().Get("query")
	}

	wait = 15 * time.Second
	s.mu.RLock()
	if s.App != nil && s.App.DefaultWait > 0 {
		wait = s.App.DefaultWait
	}
	s.mu.RUnlock()

	if v := r.URL.Query().Get("wait"); v != "" {
		if sec, err := strconv.Atoi(v); err == nil && sec > 0 {
			wait = time.Duration(sec) * time.Second
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	return strings.TrimSpace(query), wait, limit
}

// searchErrorCode maps engine errors onto HTTP status codes.
func searchErrorCode(err error) int {
	switch {
	case errors.Is(err, engine.ErrEmptyQuery):
		return http.StatusBadRequest
	case errors.Is(err, engine.ErrBusy):
		return http.StatusConflict
	case errors.Is(err, engine.ErrNotReady):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if s.Engine == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "engine not started"})
		return
	}
	query, wait, limit := s.parseSearchQuery(r)

	resp, err := s.Engine.Search(r.Context(), query, engine.SearchOpts{Wait: wait, Limit: limit})
	if err != nil {
		writeJSON(w, searchErrorCode(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSearchStream serves the same query as handleSearch but pushes partial
// results over Server-Sent Events while the search is still running. Events:
//
//	open    — once, echoes the accepted query parameters
//	results — a delta of newly found hits plus running totals
//	done    — once, the authoritative SearchResponse
//	error   — once, when the search could not be started
//
// Clients that cannot stream transparently fall back to handleSearch.
func (s *Server) handleSearchStream(w http.ResponseWriter, r *http.Request) {
	if s.Engine == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "engine not started"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.handleSearch(w, r)
		return
	}
	query, wait, limit := s.parseSearchQuery(r)
	if query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": engine.ErrEmptyQuery.Error()})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	// Tell reverse proxies (nginx) not to buffer the stream.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	send := func(event string, payload any) error {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	if err := send("open", map[string]any{
		"query":    query,
		"wait_sec": int(wait / time.Second),
		"limit":    limit,
	}); err != nil {
		return
	}

	resp, err := s.Engine.SearchStream(r.Context(), query, engine.SearchOpts{Wait: wait, Limit: limit},
		func(p engine.SearchProgress) error {
			return send("results", p)
		})
	if err != nil {
		// The client hung up, or the search was aborted: the connection is gone,
		// so there is nobody left to notify.
		if errors.Is(err, context.Canceled) || errors.Is(err, io.ErrClosedPipe) {
			return
		}
		_ = send("error", map[string]any{
			"error":  err.Error(),
			"status": searchErrorCode(err),
		})
		return
	}
	_ = send("done", resp)
}

func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	if s.Engine == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "engine not started"})
		return
	}
	var req engine.ProbeRequest
	if r.Method == http.MethodPost && r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body failed"})
			return
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
				return
			}
		}
	}
	// Fall back to saved config when lists are empty (GET or blank POST).
	s.mu.RLock()
	if s.App != nil {
		if len(req.Servers) == 0 {
			req.Servers = append([]string(nil), s.App.Servers...)
		}
		if len(req.ServerMetURLs) == 0 {
			req.ServerMetURLs = append([]string(nil), s.App.ServerMetURLs...)
		}
		if len(req.NodesDatURLs) == 0 {
			req.NodesDatURLs = append([]string(nil), s.App.NodesDatURLs...)
		}
	}
	s.mu.RUnlock()

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.Engine.ProbeSources(ctx, req))
}

func (s *Server) handleServerMet(w http.ResponseWriter, r *http.Request) {
	if s.Engine == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "engine not started"})
		return
	}
	var urls []string
	limit := 0
	if r.Method == http.MethodPost && r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body failed"})
			return
		}
		if len(body) > 0 {
			var req struct {
				ServerMetURLs []string `json:"server_met_urls"`
				Limit         int      `json:"limit"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
				return
			}
			urls = req.ServerMetURLs
			limit = req.Limit
		}
	}
	s.mu.RLock()
	if s.App != nil && len(urls) == 0 {
		urls = append([]string(nil), s.App.ServerMetURLs...)
	}
	s.mu.RUnlock()
	// limit <= 0: return all ranked entries.
	writeJSON(w, http.StatusOK, s.Engine.ListServerMetAddresses(urls, limit))
}

func (s *Server) handle115Status(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	if s.App == nil {
		s.mu.RUnlock()
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "config unavailable"})
		return
	}
	cfg := s.App.Offline115
	s.mu.RUnlock()

	st := cfg.PublicStatus()
	if st.Configured && strings.TrimSpace(cfg.WPPathName) == "" {
		if list, err := cloud115.ListFolders(cfg, "", st.WPPathID); err == nil {
			st.WPPathName = cloud115.PathLabel(list.Path)
			s.mu.Lock()
			if s.App != nil && s.App.Offline115.WPPathID == st.WPPathID && strings.TrimSpace(s.App.Offline115.WPPathName) == "" {
				s.App.Offline115.WPPathName = st.WPPathName
				_ = s.App.Save()
			}
			s.mu.Unlock()
		}
	}
	if strings.TrimSpace(st.WPPathName) == "" {
		if st.WPPathID == "0" {
			st.WPPathName = "根目录"
		} else {
			st.WPPathName = "文件夹 " + st.WPPathID
		}
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handle115Folders(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	cfg := cloud115.Config{}
	if s.App != nil {
		cfg = s.App.Offline115
	}
	s.mu.RUnlock()

	cid := strings.TrimSpace(r.URL.Query().Get("cid"))
	cookie := ""
	if r.Method == http.MethodPost && r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body failed"})
			return
		}
		if len(body) > 0 {
			var req struct {
				CID    string `json:"cid"`
				Cookie string `json:"cookie"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
				return
			}
			if strings.TrimSpace(req.CID) != "" {
				cid = strings.TrimSpace(req.CID)
			}
			cookie = strings.TrimSpace(req.Cookie)
		}
	}
	list, err := cloud115.ListFolders(cfg, cookie, cid)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handle115Offline(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	cfg := cloud115.Config{}
	if s.App != nil {
		cfg = s.App.Offline115
	}
	s.mu.RUnlock()

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body failed"})
		return
	}
	var req struct {
		URLs     []string `json:"urls"`
		URL      string   `json:"url"`
		WPPathID string   `json:"wp_path_id"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
	}
	urls := append([]string(nil), req.URLs...)
	if u := strings.TrimSpace(req.URL); u != "" {
		urls = append(urls, u)
	}
	if path := strings.TrimSpace(req.WPPathID); path != "" {
		cfg.WPPathID = path
	}

	results, err := cloud115.AddOfflineTasks(cfg, urls)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	okN := 0
	for _, item := range results {
		if item.OK {
			okN++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      okN == len(results) && len(results) > 0,
		"ok_count": okN,
		"count":   len(results),
		"wp_path_id": cfg.WPPathID,
		"results": results,
	})
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

package cloud115

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config holds 115 offline-download credentials.
type Config struct {
	Enabled    bool   `yaml:"enabled" json:"enabled"`
	Cookie     string `yaml:"cookie" json:"cookie"`
	WPPathID   string `yaml:"wp_path_id" json:"wp_path_id"`     // target folder cid; "0" = root
	WPPathName string `yaml:"wp_path_name" json:"wp_path_name"` // display path, e.g. 根目录 / 电影
}

// Result is one add-task outcome.
type Result struct {
	URL     string `json:"url"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

// Status is a safe public view (cookie redacted).
type Status struct {
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`
	WPPathID   string `json:"wp_path_id"`
	WPPathName string `json:"wp_path_name"`
	CookieSet  bool   `json:"cookie_set"`
}

func (c Config) PublicStatus() Status {
	cookie := strings.TrimSpace(c.Cookie)
	pathID := strings.TrimSpace(c.WPPathID)
	if pathID == "" {
		pathID = "0"
	}
	name := strings.TrimSpace(c.WPPathName)
	if name == "" {
		if pathID == "0" {
			name = "根目录"
		}
	}
	return Status{
		Enabled:    c.Enabled,
		Configured: c.Enabled && cookie != "" && looksLikeCookie(cookie),
		WPPathID:   pathID,
		WPPathName: name,
		CookieSet:  cookie != "",
	}
}

// PathLabel joins folder path segments for display.
func PathLabel(path []Folder) string {
	if len(path) == 0 {
		return "根目录"
	}
	parts := make([]string, 0, len(path))
	for _, p := range path {
		n := strings.TrimSpace(p.Name)
		if n == "" {
			if p.CID == "0" {
				n = "根目录"
			} else {
				n = p.CID
			}
		}
		parts = append(parts, n)
	}
	return strings.Join(parts, " / ")
}

func looksLikeCookie(cookie string) bool {
	u := strings.Contains(cookie, "UID=")
	s := strings.Contains(cookie, "SEID=") || strings.Contains(cookie, "CID=")
	return u && s
}

// Folder is one 115 directory entry.
type Folder struct {
	CID  string `json:"cid"`
	Name string `json:"name"`
	PID  string `json:"pid,omitempty"`
}

// FolderList is a browsable directory listing.
type FolderList struct {
	CID     string   `json:"cid"`
	Path    []Folder `json:"path"`
	Folders []Folder `json:"folders"`
	Count   int      `json:"count"`
}

// ListFolders lists subfolders under cid (default "0" = root).
// Cookie may be passed explicitly (unsaved form value) or taken from cfg.
func ListFolders(cfg Config, cookieOverride, cid string) (FolderList, error) {
	cookie := strings.TrimSpace(cookieOverride)
	if cookie == "" {
		cookie = strings.TrimSpace(cfg.Cookie)
	}
	if cookie == "" || !looksLikeCookie(cookie) {
		return FolderList{}, fmt.Errorf("请先填写有效的 115 Cookie（需含 UID / CID / SEID）")
	}
	cid = strings.TrimSpace(cid)
	if cid == "" {
		cid = "0"
	}

	client := &http.Client{Timeout: 30 * time.Second}
	q := url.Values{}
	q.Set("aid", "1")
	q.Set("cid", cid)
	q.Set("o", "file_name")
	q.Set("asc", "1")
	q.Set("offset", "0")
	q.Set("show_dir", "1")
	q.Set("limit", "1000")
	q.Set("code", "")
	q.Set("scid", "")
	q.Set("snap", "0")
	q.Set("natsort", "1")
	q.Set("source", "")
	q.Set("format", "json")

	endpoint := "https://webapi.115.com/files?" + q.Encode()
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return FolderList{}, err
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://115.com/")

	resp, err := client.Do(req)
	if err != nil {
		return FolderList{}, fmt.Errorf("请求 115 文件夹失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return FolderList{}, err
	}
	if resp.StatusCode >= 400 {
		return FolderList{}, fmt.Errorf("115 HTTP %d: %s", resp.StatusCode, truncate(string(raw), 180))
	}

	var payload struct {
		State   any `json:"state"`
		Errno   any `json:"errno"`
		Error   any `json:"error"`
		ErrMsg  any `json:"error_msg"`
		Count   int `json:"count"`
		Path    []struct {
			CID  any    `json:"cid"`
			Name string `json:"name"`
			PID  any    `json:"pid"`
		} `json:"path"`
		Data []struct {
			CID  any    `json:"cid"`
			FID  any    `json:"fid"`
			PID  any    `json:"pid"`
			Name string `json:"n"`
			FC   any    `json:"fc"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return FolderList{}, fmt.Errorf("解析 115 文件夹失败: %w", err)
	}
	if !truthy(payload.State) {
		msg := firstNonEmpty(fmt.Sprint(payload.ErrMsg), fmt.Sprint(payload.Error), fmt.Sprintf("errno=%v", payload.Errno))
		if msg == "" || msg == "<nil>" {
			msg = "无法列出文件夹，请检查 Cookie 是否有效"
		}
		return FolderList{}, fmt.Errorf("%s", msg)
	}

	out := FolderList{
		CID:     cid,
		Path:    make([]Folder, 0, len(payload.Path)+1),
		Folders: make([]Folder, 0, 32),
	}
	if len(payload.Path) == 0 {
		out.Path = append(out.Path, Folder{CID: "0", Name: "根目录"})
	} else {
		for _, p := range payload.Path {
			out.Path = append(out.Path, Folder{
				CID:  anyString(p.CID),
				Name: displayName(p.Name, anyString(p.CID)),
				PID:  anyString(p.PID),
			})
		}
	}
	for _, item := range payload.Data {
		fcFile := false
		switch t := item.FC.(type) {
		case float64:
			fcFile = t == 1
		case string:
			fcFile = strings.TrimSpace(t) == "1"
		}
		if fcFile {
			continue
		}
		id := anyString(item.CID)
		fid := anyString(item.FID)
		if id == "" {
			continue
		}
		// Prefer explicit folders (fc=0); otherwise cid without being a pure file entry.
		if !isFolder(item.FC) && fid != "" && fid != id {
			continue
		}
		out.Folders = append(out.Folders, Folder{
			CID:  id,
			Name: displayName(item.Name, id),
			PID:  anyString(item.PID),
		})
	}
	out.Count = len(out.Folders)
	return out, nil
}

func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return true // some 115 payloads omit state when ok
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t == "1" || strings.EqualFold(t, "true")
	default:
		return true
	}
}

func isFolder(fc any) bool {
	switch t := fc.(type) {
	case float64:
		return t == 0
	case string:
		s := strings.TrimSpace(t)
		return s == "0"
	case nil:
		return false
	default:
		return fmt.Sprint(t) == "0"
	}
}

func anyString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%.0f", t)
		}
		return strings.TrimSpace(fmt.Sprint(t))
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func displayName(name, fallback string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		if fallback == "0" {
			return "根目录"
		}
		return fallback
	}
	return name
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v != "" && v != "<nil>" {
			return v
		}
	}
	return ""
}

// AddOfflineTasks pushes ed2k/magnet/http links into 115 offline download.
func AddOfflineTasks(cfg Config, urls []string) ([]Result, error) {
	if !cfg.Enabled {
		return nil, fmt.Errorf("115 离线未启用")
	}
	cookie := strings.TrimSpace(cfg.Cookie)
	if cookie == "" || !looksLikeCookie(cookie) {
		return nil, fmt.Errorf("请先在设置中填写有效的 115 Cookie（需含 UID / CID / SEID）")
	}
	pathID := strings.TrimSpace(cfg.WPPathID)
	if pathID == "" {
		pathID = "0"
	}

	cleaned := make([]string, 0, len(urls))
	seen := map[string]struct{}{}
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		cleaned = append(cleaned, u)
	}
	if len(cleaned) == 0 {
		return nil, fmt.Errorf("没有可转存的链接")
	}

	client := &http.Client{Timeout: 45 * time.Second}
	out := make([]Result, 0, len(cleaned))

	// Batch when possible; fall back to single.
	if len(cleaned) == 1 {
		out = append(out, addOne(client, cookie, pathID, cleaned[0]))
		return out, nil
	}
	batch := addMany(client, cookie, pathID, cleaned)
	if batch != nil {
		return batch, nil
	}
	for _, u := range cleaned {
		out = append(out, addOne(client, cookie, pathID, u))
	}
	return out, nil
}

func addOne(client *http.Client, cookie, pathID, link string) Result {
	form := url.Values{}
	form.Set("url", link)
	form.Set("wp_path_id", pathID)
	endpoint := "https://115.com/web/lixian/?ct=lixian&ac=add_task_url"
	body, err := postForm(client, endpoint, cookie, form)
	if err != nil {
		return Result{URL: link, OK: false, Message: err.Error()}
	}
	ok, msg := parseAddResponse(body)
	return Result{URL: link, OK: ok, Message: msg}
}

func addMany(client *http.Client, cookie, pathID string, links []string) []Result {
	form := url.Values{}
	form.Set("wp_path_id", pathID)
	for i, link := range links {
		form.Set(fmt.Sprintf("url[%d]", i), link)
	}
	endpoint := "https://115.com/web/lixian/?ct=lixian&ac=add_task_urls"
	body, err := postForm(client, endpoint, cookie, form)
	if err != nil {
		return nil
	}
	ok, msg := parseAddResponse(body)
	// Some batch responses are aggregate-only; map same status to each URL.
	out := make([]Result, 0, len(links))
	for _, link := range links {
		out = append(out, Result{URL: link, OK: ok, Message: msg})
	}
	// If batch API rejected the shape, caller should fall back.
	if !ok && (strings.Contains(msg, "未知") || strings.Contains(msg, "empty") || body == "") {
		return nil
	}
	return out
}

func postForm(client *http.Client, endpoint, cookie string, form url.Values) (string, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://115.com/")
	req.Header.Set("Origin", "https://115.com")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 115 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("115 HTTP %d: %s", resp.StatusCode, truncate(string(raw), 180))
	}
	return string(raw), nil
}

func parseAddResponse(raw string) (ok bool, message string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, "115 返回空响应"
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		// Non-JSON success pages are rare; treat as failure with snippet.
		return false, "无法解析 115 响应: " + truncate(raw, 120)
	}
	if state, exists := payload["state"]; exists {
		switch v := state.(type) {
		case bool:
			ok = v
		case float64:
			ok = v != 0
		case string:
			ok = v == "1" || strings.EqualFold(v, "true")
		}
	}
	if errNo, exists := payload["errno"]; exists && !ok {
		message = fmt.Sprintf("errno=%v", errNo)
	}
	if errcode, exists := payload["errcode"]; exists && !ok {
		if message != "" {
			message += " "
		}
		message += fmt.Sprintf("errcode=%v", errcode)
	}
	for _, key := range []string{"error_msg", "error", "message", "msg"} {
		if v, okMsg := payload[key]; okMsg {
			if s := strings.TrimSpace(fmt.Sprint(v)); s != "" && s != "<nil>" {
				if message != "" {
					message += " · "
				}
				message += s
				break
			}
		}
	}
	if ok && message == "" {
		message = "已加入离线任务"
	}
	if !ok && message == "" {
		message = "转存失败"
	}
	return ok, message
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

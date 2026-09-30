package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/goed2k/core/bootstrap"
	"github.com/p2psearch/p2psearch/internal/cloud115"
	"gopkg.in/yaml.v3"
)

// PriorityServers is the Sept 2026 high-activity shortlist.
var PriorityServers = []string{
	"176.123.5.89:4725",   // eMule Sunrise
	"85.17.116.222:6082",  // ed2k-rust main
	"91.208.162.87:4232",  // Sharing-Devils No.4
	"77.42.68.79:4232",    // Nordic Server
	"91.208.162.182:4232", // MO-Server
	"85.121.5.137:4232",   // Sharing-Devils No.2
}

const DefaultServerMet = "http://upd.emule-security.org/server.met"
const DefaultNodesDat = "http://www.alldivx.de/nodes/nodes.dat,https://upd.emule-security.org/nodes.dat"

// File is the on-disk config.yaml schema.
type File struct {
	HTTP struct {
		Addr string `yaml:"addr"`
	} `yaml:"http"`

	Proxy struct {
		// URL examples:
		//   http://127.0.0.1:7890
		//   socks5://127.0.0.1:7890
		URL string `yaml:"url"`
	} `yaml:"proxy"`

	Engine struct {
		ListenPort int  `yaml:"listen_port"`
		UDPPort    int  `yaml:"udp_port"`
		EnableKAD  bool `yaml:"enable_kad"`
		EnableUPnP bool `yaml:"enable_upnp"`
	} `yaml:"engine"`

	Bootstrap struct {
		Servers            []string `yaml:"servers"`
		AllServers         []string `yaml:"all_servers"`
		ServerMetURLs      []string `yaml:"server_met_urls"`
		NodesDatURLs       []string `yaml:"nodes_dat_urls"`
		MaxPriorityServers int      `yaml:"max_priority_servers"`
		MaxTotalServers    int      `yaml:"max_total_servers"`
		RefreshHours       int      `yaml:"refresh_hours"`
	} `yaml:"bootstrap"`

	Search struct {
		DefaultWaitSec int `yaml:"default_wait_sec"`
		DefaultLimit   int `yaml:"default_limit"`
		MaxLimit       int `yaml:"max_limit"`
	} `yaml:"search"`

	Offline115 cloud115.Config `yaml:"offline_115"`
}

// App is the runtime config used by main/engine/api.
type App struct {
	ConfigPath         string
	HTTPAddr           string
	ProxyURL           string
	ListenPort         int
	UDPPort            int
	EnableKAD          bool
	EnableUPnP         bool
	Servers            []string
	AllServers         []string
	ServerMetURLs      []string
	NodesDatURLs       []string
	MaxPriorityServers int
	MaxTotalServers    int
	RefreshInterval    time.Duration
	DefaultWait        time.Duration
	DefaultLimit       int
	MaxLimit           int
	Offline115         cloud115.Config
}

// Default returns MVP defaults aligned with the project plan.
func Default() App {
	return App{
		HTTPAddr:           "0.0.0.0:8080",
		ListenPort:         4661,
		UDPPort:            4662,
		EnableKAD:          true,
		EnableUPnP:         false,
		Servers:            append([]string(nil), PriorityServers...),
		ServerMetURLs:      []string{DefaultServerMet},
		NodesDatURLs:       strings.Split(DefaultNodesDat, ","),
		MaxPriorityServers: 8,
		MaxTotalServers:    24,
		RefreshInterval:    8 * time.Hour,
		DefaultWait:        20 * time.Second,
		DefaultLimit:       0,
		MaxLimit:           10000,
		Offline115: cloud115.Config{
			Enabled:  false,
			WPPathID: "0",
		},
	}
}

// Load reads config.yaml (optional) then applies environment overrides.
func Load(path string) (App, error) {
	app := Default()
	if path == "" {
		path = envOr("CONFIG_PATH", "config.yaml")
	}
	if _, err := os.Stat(path); err == nil {
		data, err := os.ReadFile(path)
		if err != nil {
			return app, fmt.Errorf("read config: %w", err)
		}
		f := defaultFile()
		if err := yaml.Unmarshal(data, &f); err != nil {
			return app, fmt.Errorf("parse config: %w", err)
		}
		app = fileToApp(f)
	} else if !os.IsNotExist(err) {
		return app, err
	}
	applyEnv(&app)
	normalize(&app)
	app.ConfigPath = path
	return app, nil
}

func defaultFile() File {
	a := Default()
	var f File
	f.HTTP.Addr = a.HTTPAddr
	f.Proxy.URL = a.ProxyURL
	f.Engine.ListenPort = a.ListenPort
	f.Engine.UDPPort = a.UDPPort
	f.Engine.EnableKAD = a.EnableKAD
	f.Engine.EnableUPnP = a.EnableUPnP
	f.Bootstrap.Servers = append([]string(nil), a.Servers...)
	f.Bootstrap.AllServers = append([]string(nil), a.AllServers...)
	f.Bootstrap.ServerMetURLs = append([]string(nil), a.ServerMetURLs...)
	f.Bootstrap.NodesDatURLs = append([]string(nil), a.NodesDatURLs...)
	f.Bootstrap.MaxPriorityServers = a.MaxPriorityServers
	f.Bootstrap.MaxTotalServers = a.MaxTotalServers
	f.Bootstrap.RefreshHours = int(a.RefreshInterval / time.Hour)
	f.Search.DefaultWaitSec = int(a.DefaultWait / time.Second)
	f.Search.DefaultLimit = a.DefaultLimit
	f.Search.MaxLimit = a.MaxLimit
	f.Offline115 = a.Offline115
	return f
}

func fileToApp(f File) App {
	app := App{
		HTTPAddr:           f.HTTP.Addr,
		ProxyURL:           strings.TrimSpace(f.Proxy.URL),
		ListenPort:         f.Engine.ListenPort,
		UDPPort:            f.Engine.UDPPort,
		EnableKAD:          f.Engine.EnableKAD,
		EnableUPnP:         f.Engine.EnableUPnP,
		Servers:            append([]string(nil), f.Bootstrap.Servers...),
		AllServers:         append([]string(nil), f.Bootstrap.AllServers...),
		ServerMetURLs:      append([]string(nil), f.Bootstrap.ServerMetURLs...),
		NodesDatURLs:       append([]string(nil), f.Bootstrap.NodesDatURLs...),
		MaxPriorityServers: f.Bootstrap.MaxPriorityServers,
		MaxTotalServers:    f.Bootstrap.MaxTotalServers,
		RefreshInterval:    time.Duration(f.Bootstrap.RefreshHours) * time.Hour,
		DefaultWait:        time.Duration(f.Search.DefaultWaitSec) * time.Second,
		DefaultLimit:       f.Search.DefaultLimit,
		MaxLimit:           f.Search.MaxLimit,
		Offline115:         f.Offline115,
	}
	return app
}

func applyEnv(app *App) {
	if v := envOr("HTTP_ADDR", ""); v != "" {
		app.HTTPAddr = v
	}
	if v := envOr("HTTP_PORT", ""); v != "" {
		app.HTTPAddr = ":" + v
	}
	// Prefer explicit app proxy; fall back to common env names if unset.
	if v := envOr("P2PSEARCH_PROXY", envOr("PROXY_URL", "")); v != "" {
		if isProxyDisabled(v) {
			app.ProxyURL = ""
		} else {
			app.ProxyURL = v
		}
	} else if app.ProxyURL == "" {
		if v := envOr("ALL_PROXY", envOr("all_proxy", envOr("HTTPS_PROXY", envOr("HTTP_PROXY", "")))); v != "" && !isProxyDisabled(v) {
			app.ProxyURL = v
		}
	}
	if v := envOr("LISTEN_PORT", envOr("GOED2K_LISTEN_PORT", "")); v != "" {
		fmt.Sscanf(v, "%d", &app.ListenPort)
	}
	if v := envOr("UDP_PORT", envOr("GOED2K_UDP_PORT", "")); v != "" {
		fmt.Sscanf(v, "%d", &app.UDPPort)
	}
	if v := envOr("GOED2K_KAD", envOr("P2PSEARCH_KAD", "")); v != "" {
		app.EnableKAD = parseBool(v, app.EnableKAD)
	}
	if v := envOr("GOED2K_UPNP", envOr("P2PSEARCH_UPNP", "")); v != "" {
		app.EnableUPnP = parseBool(v, app.EnableUPnP)
	}
	if v := envOr("GOED2K_SERVERS", envOr("P2PSEARCH_SERVERS", "")); v != "" {
		app.Servers = bootstrap.SplitCommaList(v)
	}
	if v := envOr("GOED2K_SERVER_MET", envOr("P2PSEARCH_SERVER_MET", "")); v != "" {
		app.ServerMetURLs = bootstrap.SplitCommaList(v)
	}
	if v := envOr("GOED2K_KAD_NODES_DAT", ""); v != "" {
		app.NodesDatURLs = bootstrap.SplitCommaList(v)
	}
}

func normalize(app *App) {
	if app.HTTPAddr == "" {
		app.HTTPAddr = "0.0.0.0:8080"
	}
	if app.ListenPort <= 0 {
		app.ListenPort = 4661
	}
	if app.UDPPort <= 0 {
		app.UDPPort = 4662
	}
	if len(app.Servers) == 0 {
		app.Servers = append([]string(nil), PriorityServers...)
	}
	if app.MaxPriorityServers <= 0 {
		app.MaxPriorityServers = 8
	}
	if app.MaxPriorityServers > len(app.Servers) {
		app.MaxPriorityServers = len(app.Servers)
	}
	if app.MaxTotalServers <= 0 {
		app.MaxTotalServers = 24
	}
	if app.MaxTotalServers < app.MaxPriorityServers {
		app.MaxTotalServers = app.MaxPriorityServers
	}
	if len(app.ServerMetURLs) == 0 {
		app.ServerMetURLs = []string{DefaultServerMet}
	}
	if len(app.NodesDatURLs) == 0 {
		app.NodesDatURLs = strings.Split(DefaultNodesDat, ",")
	}
	if app.RefreshInterval <= 0 {
		app.RefreshInterval = 8 * time.Hour
	}
	if app.DefaultWait <= 0 {
		app.DefaultWait = 15 * time.Second
	}
	// DefaultLimit 0 = unlimited (capped only by MaxLimit).
	if app.DefaultLimit < 0 {
		app.DefaultLimit = 0
	}
	if app.MaxLimit <= 0 {
		app.MaxLimit = 10000
	}
	app.Offline115.Cookie = strings.TrimSpace(app.Offline115.Cookie)
	if strings.TrimSpace(app.Offline115.WPPathID) == "" {
		app.Offline115.WPPathID = "0"
	}
}

// BootstrapConfig maps App into goed2k bootstrap.Config.
func (a App) BootstrapConfig() bootstrap.Config {
	cfg := bootstrap.DefaultConfig()
	cfg.ListenPort = a.ListenPort
	cfg.UDPPort = a.UDPPort
	cfg.EnableKAD = a.EnableKAD
	cfg.EnableUPnP = a.EnableUPnP
	// Advertise crypt support and try obfuscated ports when plain login is silent.
	cfg.EnableCryptLayer = true
	cfg.DisableState = true
	cfg.StatePath = ""
	cfg.OutDir = envOr("GOED2K_OUTDIR", "/tmp/p2psearch-downloads")

	// Server connects are handled by engine (concurrent + capped).
	// Keep ServerAddr/ServerMetPath empty here so bootstrap.RunBackground
	// only boots KAD and does not serially dial the whole server.met list.
	cfg.ServerAddr = ""
	cfg.ServerMetPath = ""
	cfg.KADNodesDat = strings.Join(a.NodesDatURLs, ",")
	return cfg
}

// PrioritySlice returns the first MaxPriorityServers addresses.
func (a App) PrioritySlice() []string {
	n := a.MaxPriorityServers
	if n > len(a.Servers) {
		n = len(a.Servers)
	}
	if n <= 0 {
		return nil
	}
	return append([]string(nil), a.Servers[:n]...)
}

// ClampLimit applies default/max limits. limit <= 0 uses DefaultLimit;
// DefaultLimit <= 0 means unlimited up to MaxLimit.
func (a App) ClampLimit(limit int) int {
	if limit <= 0 {
		if a.DefaultLimit > 0 {
			limit = a.DefaultLimit
		} else {
			limit = a.MaxLimit
		}
	}
	if a.MaxLimit > 0 && limit > a.MaxLimit {
		limit = a.MaxLimit
	}
	return limit
}

// PublicView is the JSON shape for the settings UI / API.
type PublicView struct {
	ConfigPath         string   `json:"config_path"`
	HTTPAddr           string   `json:"http_addr"`
	ProxyURL           string   `json:"proxy_url"`
	ListenPort         int      `json:"listen_port"`
	UDPPort            int      `json:"udp_port"`
	EnableKAD          bool     `json:"enable_kad"`
	EnableUPnP         bool     `json:"enable_upnp"`
	Servers            []string `json:"servers"`
	AllServers         []string `json:"all_servers"`
	ServerMetURLs      []string `json:"server_met_urls"`
	NodesDatURLs       []string `json:"nodes_dat_urls"`
	MaxPriorityServers int      `json:"max_priority_servers"`
	MaxTotalServers    int      `json:"max_total_servers"`
	RefreshHours       int      `json:"refresh_hours"`
	DefaultWaitSec     int      `json:"default_wait_sec"`
	DefaultLimit       int      `json:"default_limit"`
	MaxLimit           int      `json:"max_limit"`
	Offline115Enabled  bool     `json:"offline_115_enabled"`
	Offline115Cookie   string   `json:"offline_115_cookie"`
	Offline115WPPathID string   `json:"offline_115_wp_path_id"`
	Offline115WPPathName string `json:"offline_115_wp_path_name"`
	Offline115         cloud115.Status `json:"offline_115"`
	RestartRequired    bool     `json:"restart_required,omitempty"`
	Applied            []string `json:"applied,omitempty"`
	NeedRestart        []string `json:"need_restart,omitempty"`
	Note               string   `json:"note,omitempty"`
}

// Public returns a settings-safe view of the current app config.
func (a App) Public() PublicView {
	return PublicView{
		ConfigPath:         a.ConfigPath,
		HTTPAddr:           a.HTTPAddr,
		ProxyURL:           a.ProxyURL,
		ListenPort:         a.ListenPort,
		UDPPort:            a.UDPPort,
		EnableKAD:          a.EnableKAD,
		EnableUPnP:         a.EnableUPnP,
		Servers:            append([]string(nil), a.Servers...),
		AllServers:         append([]string(nil), a.AllServers...),
		ServerMetURLs:      append([]string(nil), a.ServerMetURLs...),
		NodesDatURLs:       append([]string(nil), a.NodesDatURLs...),
		MaxPriorityServers: a.MaxPriorityServers,
		MaxTotalServers:    a.MaxTotalServers,
		RefreshHours:       int(a.RefreshInterval / time.Hour),
		DefaultWaitSec:     int(a.DefaultWait / time.Second),
		DefaultLimit:       a.DefaultLimit,
		MaxLimit:           a.MaxLimit,
		Offline115Enabled:  a.Offline115.Enabled,
		Offline115Cookie:   a.Offline115.Cookie,
		Offline115WPPathID: a.Offline115.WPPathID,
		Offline115WPPathName: a.Offline115.WPPathName,
		Offline115:         a.Offline115.PublicStatus(),
		Note:               "代理 / 服务器 / server.met / nodes.dat / 搜索参数 / 115 网盘保存后立即生效；TCP/UDP 端口、KAD、UPnP 仍需重启。",
	}
}

// UpdateRequest is the JSON body for PUT /api/config.
type UpdateRequest struct {
	ProxyURL           *string  `json:"proxy_url"`
	ListenPort         *int     `json:"listen_port"`
	UDPPort            *int     `json:"udp_port"`
	EnableKAD          *bool    `json:"enable_kad"`
	EnableUPnP         *bool    `json:"enable_upnp"`
	Servers            []string `json:"servers"`
	AllServers         []string `json:"all_servers"`
	ServerMetURLs      []string `json:"server_met_urls"`
	NodesDatURLs       []string `json:"nodes_dat_urls"`
	MaxPriorityServers *int     `json:"max_priority_servers"`
	MaxTotalServers    *int     `json:"max_total_servers"`
	RefreshHours       *int     `json:"refresh_hours"`
	DefaultWaitSec     *int     `json:"default_wait_sec"`
	DefaultLimit       *int     `json:"default_limit"`
	MaxLimit           *int     `json:"max_limit"`
	Offline115Enabled  *bool    `json:"offline_115_enabled"`
	Offline115Cookie   *string  `json:"offline_115_cookie"`
	Offline115WPPathID *string  `json:"offline_115_wp_path_id"`
	Offline115WPPathName *string `json:"offline_115_wp_path_name"`
}

// ApplyUpdate merges a settings update into the runtime app.
func (a *App) ApplyUpdate(req UpdateRequest) {
	if req.ProxyURL != nil {
		a.ProxyURL = strings.TrimSpace(*req.ProxyURL)
	}
	if req.ListenPort != nil {
		a.ListenPort = *req.ListenPort
	}
	if req.UDPPort != nil {
		a.UDPPort = *req.UDPPort
	}
	if req.EnableKAD != nil {
		a.EnableKAD = *req.EnableKAD
	}
	if req.EnableUPnP != nil {
		a.EnableUPnP = *req.EnableUPnP
	}
	if req.Servers != nil {
		a.Servers = cleanStringList(req.Servers)
	}
	if req.AllServers != nil {
		a.AllServers = cleanStringList(req.AllServers)
	}
	if req.ServerMetURLs != nil {
		a.ServerMetURLs = cleanStringList(req.ServerMetURLs)
	}
	if req.NodesDatURLs != nil {
		a.NodesDatURLs = cleanStringList(req.NodesDatURLs)
	}
	if req.MaxPriorityServers != nil {
		a.MaxPriorityServers = *req.MaxPriorityServers
	}
	if req.MaxTotalServers != nil {
		a.MaxTotalServers = *req.MaxTotalServers
	}
	if req.RefreshHours != nil && *req.RefreshHours > 0 {
		a.RefreshInterval = time.Duration(*req.RefreshHours) * time.Hour
	}
	if req.DefaultWaitSec != nil && *req.DefaultWaitSec > 0 {
		a.DefaultWait = time.Duration(*req.DefaultWaitSec) * time.Second
	}
	if req.DefaultLimit != nil && *req.DefaultLimit >= 0 {
		a.DefaultLimit = *req.DefaultLimit
	}
	if req.MaxLimit != nil && *req.MaxLimit > 0 {
		a.MaxLimit = *req.MaxLimit
	}
	if req.Offline115Enabled != nil {
		a.Offline115.Enabled = *req.Offline115Enabled
	}
	if req.Offline115Cookie != nil {
		a.Offline115.Cookie = strings.TrimSpace(*req.Offline115Cookie)
	}
	if req.Offline115WPPathID != nil {
		a.Offline115.WPPathID = strings.TrimSpace(*req.Offline115WPPathID)
	}
	if req.Offline115WPPathName != nil {
		a.Offline115.WPPathName = strings.TrimSpace(*req.Offline115WPPathName)
	}
	normalize(a)
}

// Save writes the current app config back to ConfigPath as YAML.
func (a App) Save() error {
	path := strings.TrimSpace(a.ConfigPath)
	if path == "" {
		path = "config.yaml"
	}
	f := File{}
	f.HTTP.Addr = a.HTTPAddr
	f.Proxy.URL = a.ProxyURL
	f.Engine.ListenPort = a.ListenPort
	f.Engine.UDPPort = a.UDPPort
	f.Engine.EnableKAD = a.EnableKAD
	f.Engine.EnableUPnP = a.EnableUPnP
	f.Bootstrap.Servers = append([]string(nil), a.Servers...)
	f.Bootstrap.AllServers = append([]string(nil), a.AllServers...)
	f.Bootstrap.ServerMetURLs = append([]string(nil), a.ServerMetURLs...)
	f.Bootstrap.NodesDatURLs = append([]string(nil), a.NodesDatURLs...)
	f.Bootstrap.MaxPriorityServers = a.MaxPriorityServers
	f.Bootstrap.MaxTotalServers = a.MaxTotalServers
	f.Bootstrap.RefreshHours = int(a.RefreshInterval / time.Hour)
	f.Search.DefaultWaitSec = int(a.DefaultWait / time.Second)
	f.Search.DefaultLimit = a.DefaultLimit
	f.Search.MaxLimit = a.MaxLimit
	f.Offline115 = a.Offline115

	data, err := yaml.Marshal(&f)
	if err != nil {
		return err
	}
	header := []byte("# P2P Search config (written by settings UI)\n")
	return os.WriteFile(path, append(header, data...), 0o644)
}

func cleanStringList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func parseBool(v string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func isProxyDisabled(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "none", "direct", "disable", "disabled", "-":
		return true
	default:
		return false
	}
}

package cpanelplugin

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"os"
	"time"
)

// CGI front, served by cpsrvd in two roles selected by MXS_PLUGIN_MODE:
//
//   - "whm"  → the WHM admin relay-installer. Runs as root (whostmgr docroot ⇒ admin-only),
//     reads the API token directly, and performs the privileged Exim mutations.
//   - else   → the cPanel end-user status page. Runs as the account uid and only proxies a
//     read-only summary to the root broker, which scopes the response by peer uid.
const (
	EnvSocketPath = "MXS_PLUGIN_SOCKET" // tells the user CGI where the broker socket is
	EnvMode       = "MXS_PLUGIN_MODE"   // "whm" selects the admin tool
	EnvConfig     = "MXS_PLUGIN_CONFIG" // broker/admin config path (root-only)
)

// RunCGI serves the plugin as a CGI program under cpsrvd.
func RunCGI(socketPath string) error {
	if os.Getenv(EnvMode) == "whm" {
		return cgi.Serve(&whmHandler{})
	}

	if socketPath == "" {
		socketPath = os.Getenv(EnvSocketPath)
	}
	if socketPath == "" {
		socketPath = DefaultSocketPath
	}
	// HTTP client that always dials the broker's unix socket regardless of URL host.
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
	return cgi.Serve(&cgiHandler{client: client})
}

// ---- WHM admin handler (privileged) ---------------------------------------------

type whmHandler struct{}

func (h *whmHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	api := r.URL.Query().Get("api")
	if api == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(whmHTML)
		return
	}

	// Defense in depth: the privileged actions must only ever run as root. cpsrvd already
	// gates the whostmgr docroot to authenticated operators; this rejects any other path.
	if os.Geteuid() != 0 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "must run as root (WHM)"})
		return
	}

	cfg, err := LoadConfig(os.Getenv(EnvConfig))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config: " + err.Error()})
		return
	}
	rm := newRelayManager(cfg)
	ctx := r.Context()

	switch api {
	case "status":
		writeJSON(w, http.StatusOK, rm.Status(ctx))
	case "dns":
		recs, err := rm.DNSRecords(ctx)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"records": recs})
	case "enable":
		if !requirePOST(w, r) {
			return
		}
		st, err := rm.Enable(ctx)
		respondAction(w, st, err)
	case "disable":
		if !requirePOST(w, r) {
			return
		}
		st, err := rm.Disable(ctx)
		respondAction(w, st, err)
	case "test":
		if !requirePOST(w, r) {
			return
		}
		var body struct {
			To string `json:"to"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
		writeJSON(w, http.StatusOK, rm.Test(ctx, body.To))
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown action"})
	}
}

func requirePOST(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return false
	}
	return true
}

// respondAction returns the resulting status, attaching any action error to it so the UI
// shows both the failure and the (unchanged) state.
func respondAction(w http.ResponseWriter, st RelayStatus, err error) {
	if err != nil && st.Error == "" {
		st.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, st)
}

// ---- cPanel end-user handler (read-only proxy) ----------------------------------

type cgiHandler struct {
	client *http.Client
}

func (h *cgiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("api") != "" || containsAPISegment(r.URL.Path) {
		h.proxy(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(indexHTML)
}

func containsAPISegment(p string) bool {
	for i := 0; i+4 <= len(p); i++ {
		if p[i:i+4] == "/api" {
			return true
		}
	}
	return false
}

// proxy forwards a read-only summary request to the broker. The broker decides scope from
// the connection's peer uid — this CGI process's own uid (the authenticated cPanel user).
func (h *cgiHandler) proxy(w http.ResponseWriter, r *http.Request) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://broker/summary", nil)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	resp, err := h.client.Do(req)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"MX Sentinel broker is unavailable. Is the mxsentinel-plugin service running?"}`)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 8<<20))
}

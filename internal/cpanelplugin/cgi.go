package cpanelplugin

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"os"
	"time"
)

// EnvSocketPath lets the cpsrvd wrapper tell the CGI where the broker socket lives,
// without the CGI ever reading the root-only broker config (which holds the token).
const EnvSocketPath = "MXS_PLUGIN_SOCKET"

// RunCGI serves the plugin as a CGI program under cpsrvd. It either returns the
// static dashboard shell or proxies an /api request to the broker socket. socketPath
// is the broker unix socket; empty uses MXS_PLUGIN_SOCKET then DefaultSocketPath.
func RunCGI(socketPath string) error {
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

	h := &cgiHandler{client: client}
	return cgi.Serve(h)
}

type cgiHandler struct {
	client *http.Client
}

func (h *cgiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Anything under /api (or ?api=...) is proxied to the broker; everything else
	// serves the static dashboard. Only GET is exposed.
	if isAPIRequest(r) {
		h.proxy(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(indexHTML)
}

func isAPIRequest(r *http.Request) bool {
	if r.URL.Query().Get("api") != "" {
		return true
	}
	// PATH_INFO style: /.../index.cgi/api/summary
	return len(r.URL.Path) >= 4 && containsAPISegment(r.URL.Path)
}

func containsAPISegment(p string) bool {
	for i := 0; i+4 <= len(p); i++ {
		if p[i:i+4] == "/api" {
			return true
		}
	}
	return false
}

// proxy forwards the request to the broker. The only proxied resource today is the
// summary; the broker decides scope from the connection's peer uid, which is this
// CGI process's own uid (the authenticated cPanel/WHM user) — un-spoofable.
func (h *cgiHandler) proxy(w http.ResponseWriter, r *http.Request) {
	// http+unix: host is ignored by the custom DialContext.
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

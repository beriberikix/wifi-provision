// Package portal serves the captive-portal web UI and its two dynamic endpoints.
// It is deliberately backend-agnostic: it depends only on backend.NetworkBackend
// plus a small set of callbacks for the connect lifecycle, so it can be driven by
// the real Ubuntu Core backend or a fake in tests.
package portal

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/beriberikix/wifi-provision/internal/backend"
)

//go:embed ui/index.html ui/style.css ui/app.js
var uiFS embed.FS

// Server hosts the portal. Construct it with New and run it with Serve.
type Server struct {
	backend backend.NetworkBackend
	gateway string

	// onConnect is invoked (in a goroutine) when a user submits credentials.
	// It owns the AP-teardown / connect / retry lifecycle so the HTTP handler can
	// return 200 immediately, matching the original tool's fire-and-forget POST.
	onConnect func(creds backend.Creds)

	// onActivity is called the first time the portal is meaningfully used (a
	// /networks fetch). It lets the orchestrator cancel its activity timeout.
	onActivity func()

	mux *http.ServeMux
}

// New builds a portal server. gateway is the IP the AP is reachable at; it is
// used as the captive-portal redirect target.
func New(b backend.NetworkBackend, gateway string, onConnect func(backend.Creds), onActivity func()) *Server {
	s := &Server{
		backend:    b,
		gateway:    gateway,
		onConnect:  onConnect,
		onActivity: onActivity,
		mux:        http.NewServeMux(),
	}

	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		// Only possible on a programming error in the embed path above.
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))

	s.mux.HandleFunc("/networks", s.handleNetworks)
	s.mux.HandleFunc("/connect", s.handleConnect)
	s.mux.Handle("/", fileServer)

	return s
}

// Handler returns the fully-wrapped http.Handler (CORS + captive-portal redirect).
// Exposed for tests via httptest.
func (s *Server) Handler() http.Handler {
	return s.withMiddleware(s.mux)
}

// Serve binds to gateway:port and blocks. It returns the http.Server's error.
func (s *Server) Serve(addr string) error {
	log.Printf("portal: serving on %s", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.ListenAndServe()
}

// handleNetworks scans and returns the visible networks as JSON. The first call
// counts as portal activity.
func (s *Server) handleNetworks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.onActivity != nil {
		s.onActivity()
	}

	nets, err := s.backend.Scan(r.Context())
	if err != nil {
		log.Printf("portal: scan failed: %v", err)
		http.Error(w, "scan failed", http.StatusInternalServerError)
		return
	}
	if nets == nil {
		nets = []backend.Network{}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(nets); err != nil {
		log.Printf("portal: encoding networks failed: %v", err)
	}
}

// handleConnect accepts credentials and kicks off the connect lifecycle. It
// returns 200 as soon as the request is well-formed; the actual join happens
// asynchronously (the AP, and thus this HTTP server, is torn down as part of it).
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var creds backend.Creds
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if creds.SSID == "" {
		http.Error(w, "ssid is required", http.StatusBadRequest)
		return
	}
	if s.onActivity != nil {
		s.onActivity()
	}

	log.Printf("portal: connect requested for SSID %q", creds.SSID)
	if s.onConnect != nil {
		go s.onConnect(creds)
	}
	w.WriteHeader(http.StatusOK)
}

// withMiddleware adds permissive CORS and the captive-portal redirect. Any request
// whose Host is not the gateway is 302-redirected to the portal root; combined
// with dnsmasq resolving all names to the gateway, this yields captive-portal
// detection on phones/laptops.
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		if host := hostname(r.Host); host != "" && host != s.gateway {
			http.Redirect(w, r, fmt.Sprintf("http://%s/", s.gateway), http.StatusFound)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// hostname strips any :port from a Host header value.
func hostname(host string) string {
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// ScanContext is a small helper for callers that want a bounded scan.
func ScanContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, timeout)
}

package portal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/beriberikix/wifi-provision/internal/backend"
)

// fakeBackend is a test double for backend.NetworkBackend.
type fakeBackend struct {
	nets    []backend.Network
	scanErr error
}

func (f *fakeBackend) Scan(context.Context) ([]backend.Network, error) {
	return f.nets, f.scanErr
}
func (f *fakeBackend) StartAP(context.Context, backend.APConfig) error { return nil }
func (f *fakeBackend) StopAP(context.Context) error                    { return nil }
func (f *fakeBackend) Connect(context.Context, backend.Creds) error    { return nil }
func (f *fakeBackend) Connectivity(context.Context, int) (bool, error) { return true, nil }

func newTestServer(b backend.NetworkBackend, onConnect func(backend.Creds), onActivity func()) *httptest.Server {
	// Use 127.0.0.1 as the gateway so the captive-portal redirect (which fires when
	// the request Host != gateway) does not redirect httptest's loopback requests.
	s := New(b, "127.0.0.1", onConnect, onActivity)
	return httptest.NewServer(s.Handler())
}

func TestNetworksEndpoint(t *testing.T) {
	fb := &fakeBackend{nets: []backend.Network{
		{SSID: "HomeNet", Security: backend.SecurityWPA},
		{SSID: "CorpNet", Security: backend.SecurityEnterprise},
	}}
	var activity bool
	ts := newTestServer(fb, nil, func() { activity = true })
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/networks")
	if err != nil {
		t.Fatalf("GET /networks: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got []backend.Network
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0].SSID != "HomeNet" || got[1].Security != backend.SecurityEnterprise {
		t.Errorf("unexpected networks: %+v", got)
	}
	if !activity {
		t.Error("onActivity was not called on /networks")
	}
}

func TestNetworksEmptyIsArray(t *testing.T) {
	ts := newTestServer(&fakeBackend{nets: nil}, nil, nil)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/networks")
	if err != nil {
		t.Fatalf("GET /networks: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 8)
	n, _ := resp.Body.Read(buf)
	if !strings.HasPrefix(strings.TrimSpace(string(buf[:n])), "[]") {
		t.Errorf("empty scan should encode as [], got %q", string(buf[:n]))
	}
}

func TestConnectEndpoint(t *testing.T) {
	got := make(chan backend.Creds, 1)
	ts := newTestServer(&fakeBackend{}, func(c backend.Creds) { got <- c }, nil)
	defer ts.Close()

	body := strings.NewReader(`{"ssid":"HomeNet","identity":"","passphrase":"pw"}`)
	resp, err := http.Post(ts.URL+"/connect", "application/json", body)
	if err != nil {
		t.Fatalf("POST /connect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	select {
	case c := <-got:
		if c.SSID != "HomeNet" || c.Passphrase != "pw" {
			t.Errorf("onConnect got %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onConnect was not invoked")
	}
}

func TestConnectRequiresSSID(t *testing.T) {
	ts := newTestServer(&fakeBackend{}, func(backend.Creds) {}, nil)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/connect", "application/json", strings.NewReader(`{"ssid":""}`))
	if err != nil {
		t.Fatalf("POST /connect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for missing ssid", resp.StatusCode)
	}
}

func TestCaptivePortalRedirect(t *testing.T) {
	s := New(&fakeBackend{}, "192.168.42.1", nil, nil)
	// Do not follow redirects; inspect the 302 directly.
	req := httptest.NewRequest(http.MethodGet, "http://example.com/generate_204", nil)
	req.Host = "connectivitycheck.example.com"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "http://192.168.42.1/" {
		t.Errorf("Location = %q, want portal root", loc)
	}
}

func TestNoRedirectForGateway(t *testing.T) {
	s := New(&fakeBackend{nets: []backend.Network{}}, "192.168.42.1", nil, nil)
	req := httptest.NewRequest(http.MethodGet, "http://192.168.42.1/networks", nil)
	req.Host = "192.168.42.1"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (no redirect for gateway host)", rec.Code)
	}
}

// ensure fakeBackend satisfies the interface at compile time
var _ backend.NetworkBackend = (*fakeBackend)(nil)

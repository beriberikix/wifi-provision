// Command portal-dev serves the captive portal against an in-memory fake backend,
// with no root, hardware, or network changes. It exists so the UI and the HTTP
// contract can be exercised on a dev machine (or a multipass VM with no radio).
//
//	go run ./cmd/portal-dev            # serves on 127.0.0.1:8080
//	go run ./cmd/portal-dev -addr :9000
package main

import (
	"context"
	"flag"
	"log"

	"github.com/beriberikix/wifi-provision/internal/backend"
	"github.com/beriberikix/wifi-provision/internal/portal"
)

type fakeBackend struct{}

func (fakeBackend) Scan(context.Context) ([]backend.Network, error) {
	return []backend.Network{
		{SSID: "Home-WiFi", Security: backend.SecurityWPA},
		{SSID: "Corp-Secure", Security: backend.SecurityEnterprise},
		{SSID: "CoffeeShop", Security: backend.SecurityNone},
	}, nil
}
func (fakeBackend) StartAP(context.Context, backend.APConfig) error { return nil }
func (fakeBackend) StopAP(context.Context) error                    { return nil }
func (fakeBackend) Connect(_ context.Context, c backend.Creds) error {
	log.Printf("[fake] Connect ssid=%q identity=%q passphrase=%q", c.SSID, c.Identity, c.Passphrase)
	return nil
}
func (fakeBackend) Connectivity(context.Context, int) (bool, error) { return true, nil }

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	flag.Parse()

	// Gateway matches the listen host so the captive-portal redirect stays quiet
	// for local browsing.
	s := portal.New(fakeBackend{}, "127.0.0.1", func(c backend.Creds) {
		log.Printf("[fake] onConnect: %+v", c)
	}, func() {
		log.Printf("[fake] onActivity")
	})

	log.Printf("portal-dev: serving fake portal on http://%s", *addr)
	if err := s.Serve(*addr); err != nil {
		log.Fatal(err)
	}
}

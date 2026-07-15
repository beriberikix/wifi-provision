// Command wifi-provision hosts a captive-portal WiFi onboarding flow for default
// Ubuntu Core. It brings up an access point, serves a portal that lists nearby
// networks, and joins the network the user selects (persisting it via netplan).
//
// Lifecycle is single-shot, mirroring the original wifi-connect: once the device
// successfully joins a network, the process exits so a supervisor (systemd/snapd)
// can leave it stopped until explicitly restarted.
package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/beriberikix/wifi-provision/internal/backend"
	"github.com/beriberikix/wifi-provision/internal/config"
	"github.com/beriberikix/wifi-provision/internal/portal"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("wifi-provision: ")

	cfg := config.LoadOrExit()

	b, err := backend.NewCore(backend.Options{Interface: cfg.Interface})
	if err != nil {
		log.Fatalf("initializing backend: %v", err)
	}
	log.Printf("using wireless interface %s", b.Interface())

	app := &app{cfg: cfg, backend: b}
	os.Exit(app.run())
}

// app owns the orchestration state shared across the portal callbacks and the
// main run loop.
type app struct {
	cfg     config.Config
	backend backend.NetworkBackend

	activityOnce sync.Once
	activityCh   chan struct{} // closed on first portal activity

	doneOnce sync.Once
	doneCh   chan error // receives the terminal outcome (nil = success)
}

func (a *app) run() int {
	a.activityCh = make(chan struct{})
	a.doneCh = make(chan error, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Bring up the captive-portal AP.
	apCfg := backend.APConfig{
		Interface:  ifaceOf(a.backend),
		SSID:       a.cfg.SSID,
		Passphrase: a.cfg.Passphrase,
		Gateway:    a.cfg.Gateway,
		DHCPRange:  a.cfg.DHCPRange,
	}
	if err := a.backend.StartAP(ctx, apCfg); err != nil {
		log.Printf("failed to start access point: %v", err)
		return 1
	}
	log.Printf("access point %q up on %s", a.cfg.SSID, a.cfg.Gateway)

	// Serve the portal in the background.
	srv := portal.New(a.backend, a.cfg.Gateway, a.onConnect, a.onActivity)
	addr := net.JoinHostPort(a.cfg.Gateway, strconv.Itoa(a.cfg.ListeningPort))
	go func() {
		if err := srv.Serve(addr); err != nil {
			// ListenAndServe always returns a non-nil error; only treat it as fatal
			// before we've reached a terminal state.
			a.finish(err)
		}
	}()

	a.startActivityTimeout()
	a.startSignalTrap()

	// Block until a terminal outcome, then clean up the AP.
	outcome := <-a.doneCh
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cleanupCancel()
	_ = a.backend.StopAP(cleanupCtx)

	if outcome != nil {
		log.Printf("exiting with error: %v", outcome)
		return 1
	}
	log.Printf("connected successfully; exiting")
	return 0
}

// onActivity is called on the first portal interaction; it cancels the activity
// timeout so a device that a user has begun configuring is not killed mid-setup.
func (a *app) onActivity() {
	a.activityOnce.Do(func() { close(a.activityCh) })
}

// onConnect runs the connect lifecycle: tear down the AP, join the chosen network,
// verify connectivity. On success it ends the process; on failure it restarts the
// AP so the user can try again.
func (a *app) onConnect(creds backend.Creds) {
	ctx := context.Background()
	log.Printf("attempting to join %q", creds.SSID)

	if err := a.backend.StopAP(ctx); err != nil {
		log.Printf("stopping AP before connect: %v", err)
	}

	if err := a.backend.Connect(ctx, creds); err != nil {
		log.Printf("connect failed: %v; restarting portal", err)
		a.restartAP(ctx)
		return
	}

	ok, err := a.backend.Connectivity(ctx, 20)
	if err != nil {
		log.Printf("connectivity check errored: %v", err)
	}
	if ok {
		log.Printf("internet connectivity established")
		a.finish(nil)
		return
	}

	log.Printf("no connectivity after joining %q; restarting portal", creds.SSID)
	a.restartAP(ctx)
}

// restartAP brings the captive portal back up after a failed attempt.
func (a *app) restartAP(ctx context.Context) {
	apCfg := backend.APConfig{
		Interface:  ifaceOf(a.backend),
		SSID:       a.cfg.SSID,
		Passphrase: a.cfg.Passphrase,
		Gateway:    a.cfg.Gateway,
		DHCPRange:  a.cfg.DHCPRange,
	}
	if err := a.backend.StartAP(ctx, apCfg); err != nil {
		log.Printf("failed to restart access point: %v", err)
		a.finish(err)
	}
}

// startActivityTimeout exits the process if the portal sees no activity within
// the configured window. A zero timeout disables it.
func (a *app) startActivityTimeout() {
	if a.cfg.ActivityTimeout <= 0 {
		return
	}
	go func() {
		select {
		case <-a.activityCh:
			// Portal used in time; let it run.
		case <-time.After(time.Duration(a.cfg.ActivityTimeout) * time.Second):
			log.Printf("no portal activity within %ds; exiting", a.cfg.ActivityTimeout)
			a.finish(nil)
		}
	}()
}

// startSignalTrap ends the run cleanly on SIGINT/SIGTERM.
func (a *app) startSignalTrap() {
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		s := <-sig
		log.Printf("received signal %s; shutting down", s)
		a.finish(nil)
	}()
}

// finish records the terminal outcome exactly once, unblocking run().
func (a *app) finish(err error) {
	a.doneOnce.Do(func() { a.doneCh <- err })
}

// ifaceOf returns the interface a backend operates on, when it exposes one.
func ifaceOf(b backend.NetworkBackend) string {
	if c, ok := b.(interface{ Interface() string }); ok {
		return c.Interface()
	}
	return ""
}

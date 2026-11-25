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

	if cfg.SkipIfProvisioned {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		provisioned, err := backend.WiFiProvisioned(ctx)
		cancel()
		switch {
		case err != nil:
			// Can't tell. Don't risk taking a working connection down; e.g. in
			// a snap whose plugs aren't connected yet, netplan can't even run.
			log.Printf("checking for existing WiFi config: %v; not starting the portal", err)
			os.Exit(exitError)
		case provisioned:
			log.Printf("WiFi already provisioned in netplan; not starting the portal")
			os.Exit(exitOK)
		}
	}

	b, err := newBackend(cfg)
	if err != nil {
		log.Fatalf("initializing backend: %v", err)
	}
	log.Printf("using wireless interface %s", b.Interface())

	app := &app{cfg: cfg, backend: b}
	os.Exit(app.run())
}

// interfaceWait bounds how long to wait for a wireless interface to appear when
// auto-detecting. At boot the service can start before the WiFi driver (e.g.
// brcmfmac on a Pi) has created the interface.
const interfaceWait = 60 * time.Second

// newBackend builds the backend, retrying interface auto-detection until
// interfaceWait passes. An explicitly configured interface is not waited for.
func newBackend(cfg config.Config) (*backend.CoreBackend, error) {
	opts := backend.Options{Interface: cfg.Interface, Country: cfg.Country}
	deadline := time.Now().Add(interfaceWait)
	for {
		b, err := backend.NewCore(opts)
		if err == nil || cfg.Interface != "" || time.Now().After(deadline) {
			return b, err
		}
		time.Sleep(2 * time.Second)
	}
}

// app owns the orchestration state shared across the portal callbacks and the
// main run loop.
type app struct {
	cfg     config.Config
	backend backend.NetworkBackend

	activityOnce sync.Once
	activityCh   chan struct{} // closed on first portal activity

	doneOnce sync.Once
	doneCh   chan outcome // receives the terminal outcome
}

// outcome is how a run ended. Only outcomeConnected means the device joined a
// network; the others leave it unprovisioned and restore the host's networking.
type outcome struct {
	kind outcomeKind
	err  error // set for outcomeError
}

type outcomeKind int

const (
	outcomeConnected outcomeKind = iota // joined a network with connectivity
	outcomeError                        // unrecoverable failure
	outcomeTimeout                      // portal unused within --activity-timeout
	outcomeSignal                       // stopped by SIGINT/SIGTERM
)

// connectTimeoutSeconds bounds how long a join may take before the portal comes
// back for a retry. Measured on a Raspberry Pi 4: ~15s from `netplan apply` to a
// DHCP lease, including one association retry, so 20s was too tight.
const connectTimeoutSeconds = 45

// Exit codes. A signal is a deliberate stop (e.g. `snap stop`), so it exits 0
// like success; a timeout gets its own code so supervisors can tell it apart.
const (
	exitOK      = 0
	exitError   = 1
	exitTimeout = 3
)

func (a *app) run() int {
	a.activityCh = make(chan struct{})
	a.doneCh = make(chan outcome, 1)

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
		a.restoreNetworking()
		return exitError
	}
	log.Printf("access point %q up on %s", a.cfg.SSID, a.cfg.Gateway)

	// Serve the portal in the background.
	srv := portal.New(a.backend, a.cfg.Gateway, a.onConnect, a.onActivity)
	addr := net.JoinHostPort(a.cfg.Gateway, strconv.Itoa(a.cfg.ListeningPort))
	go func() {
		if err := srv.Serve(addr); err != nil {
			// ListenAndServe always returns a non-nil error; only treat it as fatal
			// before we've reached a terminal state.
			a.finish(outcome{kind: outcomeError, err: err})
		}
	}()

	a.startActivityTimeout()
	a.startSignalTrap()

	// Block until a terminal outcome, then clean up the AP.
	out := <-a.doneCh
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cleanupCancel()
	_ = a.backend.StopAP(cleanupCtx)

	if out.kind != outcomeConnected {
		a.restoreNetworking()
	}

	switch out.kind {
	case outcomeConnected:
		log.Printf("connected successfully; exiting")
		return exitOK
	case outcomeTimeout:
		log.Printf("exiting after activity timeout; device not provisioned")
		return exitTimeout
	case outcomeSignal:
		log.Printf("stopped by signal; device not provisioned")
		return exitOK
	default:
		log.Printf("exiting with error: %v", out.err)
		return exitError
	}
}

// restoreNetworking hands the radio back to the host's own networking after a
// run that did not connect, so e.g. a device already on WiFi isn't left offline.
func (a *app) restoreNetworking() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := a.backend.RestoreNetworking(ctx); err != nil {
		log.Printf("%v", err)
	}
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
		a.revertConnect(ctx)
		a.restartAP(ctx)
		return
	}

	ok, err := a.backend.Connectivity(ctx, connectTimeoutSeconds)
	if err != nil {
		log.Printf("connectivity check errored: %v", err)
	}
	if ok {
		log.Printf("internet connectivity established")
		a.finish(outcome{kind: outcomeConnected})
		return
	}

	log.Printf("no connectivity after joining %q; restarting portal", creds.SSID)
	a.revertConnect(ctx)
	a.restartAP(ctx)
}

// revertConnect drops the config from a failed join so it isn't kept (or
// re-applied when networking is restored on exit).
func (a *app) revertConnect(ctx context.Context) {
	if err := a.backend.RevertConnect(ctx); err != nil {
		log.Printf("reverting failed join: %v", err)
	}
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
		a.finish(outcome{kind: outcomeError, err: err})
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
			log.Printf("no portal activity within %ds", a.cfg.ActivityTimeout)
			a.finish(outcome{kind: outcomeTimeout})
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
		a.finish(outcome{kind: outcomeSignal})
	}()
}

// finish records the terminal outcome exactly once, unblocking run().
func (a *app) finish(o outcome) {
	a.doneOnce.Do(func() { a.doneCh <- o })
}

// ifaceOf returns the interface a backend operates on, when it exposes one.
func ifaceOf(b backend.NetworkBackend) string {
	if c, ok := b.(interface{ Interface() string }); ok {
		return c.Interface()
	}
	return ""
}

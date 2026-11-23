package backend

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RestoreNetworking hands the radio back to the host's own networking after we
// exit without connecting (timeout, signal, error). If StartAP terminated a
// pre-existing wpa_supplicant, `netplan apply` brings it back; otherwise there is
// nothing to restore.
func (c *CoreBackend) RestoreNetworking(ctx context.Context) error {
	c.mu.Lock()
	released := c.releasedSupplicant
	c.mu.Unlock()
	if !released {
		return nil
	}
	if err := run(ctx, "netplan", "apply"); err != nil {
		return fmt.Errorf("restoring networking: %w", err)
	}
	return nil
}

// apProcess bundles the running hostapd and dnsmasq children plus the temp files
// backing them, so StopAP can tear everything down cleanly.
type apProcess struct {
	iface       string
	hostapd     *child
	dnsmasq     *child
	hostapdConf string
}

// child is a supervised subprocess whose exit we can observe without racing
// a second Wait.
type child struct {
	name string
	cmd  *exec.Cmd
	done chan struct{}
	err  error // valid once done is closed
}

func startChild(name string, cmd *exec.Cmd) (*child, error) {
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", name, err)
	}
	c := &child{name: name, cmd: cmd, done: make(chan struct{})}
	go func() {
		c.err = c.cmd.Wait()
		close(c.done)
	}()
	return c, nil
}

func (c *child) stop() {
	if c == nil {
		return
	}
	_ = c.cmd.Process.Kill()
	<-c.done
}

// apStartupGrace is how long hostapd and dnsmasq must stay up before StartAP
// reports success. Both fail fast on bad config, a busy radio or a sandbox
// denial; without this check the portal would look up while phones get no
// beacon or no DHCP lease.
var apStartupGrace = 2 * time.Second

// waitStable returns an error if any child exits within grace.
func waitStable(grace time.Duration, children ...*child) error {
	deadline := time.Now().Add(grace)
	for {
		for _, c := range children {
			select {
			case <-c.done:
				return fmt.Errorf("%s exited during startup: %v (see its log above)", c.name, c.err)
			default:
			}
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// StartAP brings the interface into AP mode: it assigns the gateway IP, writes a
// hostapd config and launches hostapd, then launches dnsmasq for DHCP plus the
// wildcard-DNS captive-portal trick. The interface must first be free of
// wpa_supplicant's client role (handled by the orchestrator before StartAP).
func (c *CoreBackend) StartAP(ctx context.Context, cfg APConfig) error {
	// Scan while the radio is still free, so the portal has a network list to
	// serve while the AP occupies it. A failed scan is not fatal: the user can
	// still type an SSID, and a later retry (restartAP) rescans.
	if _, err := c.Scan(ctx); err != nil {
		log.Printf("pre-AP scan failed: %v", err)
	}

	// Stop any wpa_supplicant on the interface; otherwise it keeps pulling the
	// radio back into station mode and the AP never actually broadcasts.
	// Record a release even when it errors: TERMINATE may have taken effect with
	// only the reply lost, and RestoreNetworking must still bring networking back.
	released, err := c.releaseSupplicant()
	if released {
		c.mu.Lock()
		c.releasedSupplicant = true
		c.mu.Unlock()
	}
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ap != nil {
		return fmt.Errorf("access point already running")
	}
	iface := cfg.Interface
	if iface == "" {
		iface = c.iface
	}

	// Assign the gateway IP to the interface so clients can reach the portal.
	if err := run(ctx, "ip", "addr", "flush", "dev", iface); err != nil {
		return fmt.Errorf("flushing %s: %w", iface, err)
	}
	if err := run(ctx, "ip", "addr", "add", cfg.Gateway+"/24", "dev", iface); err != nil {
		return fmt.Errorf("assigning %s to %s: %w", cfg.Gateway, iface, err)
	}
	if err := run(ctx, "ip", "link", "set", iface, "up"); err != nil {
		return fmt.Errorf("bringing up %s: %w", iface, err)
	}

	confPath, err := writeHostapdConf(iface, cfg, c.country)
	if err != nil {
		return err
	}

	hostapd, err := startChild("hostapd", exec.CommandContext(ctx, "hostapd", confPath))
	if err != nil {
		_ = os.Remove(confPath)
		return err
	}

	dnsmasq, err := startChild("dnsmasq", exec.CommandContext(ctx, "dnsmasq", dnsmasqArgs(iface, cfg)...))
	if err != nil {
		hostapd.stop()
		_ = os.Remove(confPath)
		return err
	}

	if err := waitStable(apStartupGrace, hostapd, dnsmasq); err != nil {
		dnsmasq.stop()
		hostapd.stop()
		_ = os.Remove(confPath)
		_ = run(ctx, "ip", "addr", "flush", "dev", iface)
		return err
	}

	c.ap = &apProcess{iface: iface, hostapd: hostapd, dnsmasq: dnsmasq, hostapdConf: confPath}
	return nil
}

// StopAP kills the AP children and removes the temp hostapd config. It is safe to
// call when no AP is running.
func (c *CoreBackend) StopAP(ctx context.Context) error {
	c.mu.Lock()
	ap := c.ap
	c.ap = nil
	c.mu.Unlock()
	if ap == nil {
		return nil
	}

	ap.dnsmasq.stop()
	ap.hostapd.stop()
	if ap.hostapdConf != "" {
		_ = os.Remove(ap.hostapdConf)
	}
	// Drop the portal gateway address; otherwise it lingers on an interface
	// nothing else manages (e.g. a fresh device with no WiFi netplan config).
	_ = run(ctx, "ip", "addr", "flush", "dev", ap.iface)
	// Give the driver a moment to leave AP mode before a subsequent scan/connect.
	time.Sleep(1 * time.Second)
	return nil
}

// dnsmasqArgs builds the dnsmasq command line for the portal AP.
func dnsmasqArgs(iface string, cfg APConfig) []string {
	args := []string{
		"--keep-in-foreground",
		// The default lease file (/var/lib/misc) is read-only under snap
		// confinement and dnsmasq treats that as fatal; leases are throwaway.
		"--dhcp-leasefile=" + filepath.Join(os.TempDir(), "wifi-provision-dnsmasq.leases"),
		"--pid-file=", // no pid file: we supervise the process directly
		"--no-resolv",
		"--no-hosts",
		"--bind-interfaces",
		"--interface=" + iface,
		"--except-interface=lo",
		"--dhcp-range=" + cfg.DHCPRange,
		"--dhcp-option=option:router," + cfg.Gateway,
		"--dhcp-option=option:dns-server," + cfg.Gateway,
		// Wildcard DNS: resolve every name to the gateway to trigger captive-portal
		// detection and funnel all requests to the portal.
		"--address=/#/" + cfg.Gateway,
	}
	if os.Getenv("SNAP") != "" {
		// dnsmasq normally drops to nobody:dip, but snapd's seccomp filter
		// denies setgroups() and switching to non-root IDs, which dnsmasq treats
		// as fatal. Stay root; the snap's AppArmor profile is the sandbox.
		args = append(args, "--user=root", "--group=")
	}
	return args
}

// writeHostapdConf writes a minimal hostapd config to a temp file and returns its
// path. An empty passphrase yields an open AP; otherwise WPA2-PSK.
func writeHostapdConf(iface string, cfg APConfig, country string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "interface=%s\n", iface)
	fmt.Fprintf(&b, "ssid=%s\n", cfg.SSID)
	b.WriteString("driver=nl80211\n")
	b.WriteString("hw_mode=g\n")
	b.WriteString("channel=6\n")
	if country != "" {
		// Sets the regulatory domain and advertises it (802.11d) to clients.
		fmt.Fprintf(&b, "country_code=%s\n", country)
		b.WriteString("ieee80211d=1\n")
	}
	if cfg.Passphrase != "" {
		b.WriteString("wpa=2\n")
		fmt.Fprintf(&b, "wpa_passphrase=%s\n", cfg.Passphrase)
		b.WriteString("wpa_key_mgmt=WPA-PSK\n")
		b.WriteString("rsn_pairwise=CCMP\n")
	}

	f, err := os.CreateTemp("", "hostapd-*.conf")
	if err != nil {
		return "", fmt.Errorf("creating hostapd config: %w", err)
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("writing hostapd config: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// run executes a command, attaching stderr for diagnostics, and returns an error
// including captured output on failure.
func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

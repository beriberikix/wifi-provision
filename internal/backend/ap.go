package backend

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// apProcess bundles the running hostapd and dnsmasq children plus the temp files
// backing them, so StopAP can tear everything down cleanly.
type apProcess struct {
	hostapd     *exec.Cmd
	dnsmasq     *exec.Cmd
	hostapdConf string
}

// StartAP brings the interface into AP mode: it assigns the gateway IP, writes a
// hostapd config and launches hostapd, then launches dnsmasq for DHCP plus the
// wildcard-DNS captive-portal trick. The interface must first be free of
// wpa_supplicant's client role (handled by the orchestrator before StartAP).
func (c *CoreBackend) StartAP(ctx context.Context, cfg APConfig) error {
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

	confPath, err := writeHostapdConf(iface, cfg)
	if err != nil {
		return err
	}

	hostapd := exec.CommandContext(ctx, "hostapd", confPath)
	hostapd.Stdout, hostapd.Stderr = os.Stdout, os.Stderr
	if err := hostapd.Start(); err != nil {
		_ = os.Remove(confPath)
		return fmt.Errorf("starting hostapd: %w", err)
	}

	dnsmasq := exec.CommandContext(ctx, "dnsmasq",
		"--keep-in-foreground",
		"--no-resolv",
		"--no-hosts",
		"--bind-interfaces",
		"--interface="+iface,
		"--except-interface=lo",
		"--dhcp-range="+cfg.DHCPRange,
		"--dhcp-option=option:router,"+cfg.Gateway,
		"--dhcp-option=option:dns-server,"+cfg.Gateway,
		// Wildcard DNS: resolve every name to the gateway to trigger captive-portal
		// detection and funnel all requests to the portal.
		"--address=/#/"+cfg.Gateway,
	)
	dnsmasq.Stdout, dnsmasq.Stderr = os.Stdout, os.Stderr
	if err := dnsmasq.Start(); err != nil {
		_ = hostapd.Process.Kill()
		_, _ = hostapd.Process.Wait()
		_ = os.Remove(confPath)
		return fmt.Errorf("starting dnsmasq: %w", err)
	}

	c.ap = &apProcess{hostapd: hostapd, dnsmasq: dnsmasq, hostapdConf: confPath}
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

	for _, cmd := range []*exec.Cmd{ap.dnsmasq, ap.hostapd} {
		if cmd == nil || cmd.Process == nil {
			continue
		}
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	if ap.hostapdConf != "" {
		_ = os.Remove(ap.hostapdConf)
	}
	// Give the driver a moment to leave AP mode before a subsequent scan/connect.
	time.Sleep(1 * time.Second)
	return nil
}

// writeHostapdConf writes a minimal hostapd config to a temp file and returns its
// path. An empty passphrase yields an open AP; otherwise WPA2-PSK.
func writeHostapdConf(iface string, cfg APConfig) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "interface=%s\n", iface)
	fmt.Fprintf(&b, "ssid=%s\n", cfg.SSID)
	b.WriteString("driver=nl80211\n")
	b.WriteString("hw_mode=g\n")
	b.WriteString("channel=6\n")
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

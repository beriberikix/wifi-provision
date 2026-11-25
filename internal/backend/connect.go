package backend

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Connect persists the chosen network via netplan and applies it, which triggers
// networkd/wpa_supplicant to join as a client. The AP must already be stopped by
// the orchestrator (the interface can't be in AP mode and client mode at once).
func (c *CoreBackend) Connect(ctx context.Context, creds Creds) error {
	content := renderNetplan(c.iface, creds, c.classifyFor(creds), c.country)

	// Remember what was there so RevertConnect can undo a join that fails.
	prev, err := os.ReadFile(c.netplanFile)
	c.mu.Lock()
	c.prevNetplan, c.prevNetplanExists = prev, err == nil
	c.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(c.netplanFile), 0o755); err != nil {
		return fmt.Errorf("creating netplan dir: %w", err)
	}
	// 0600 because the file may contain a passphrase.
	if err := os.WriteFile(c.netplanFile, []byte(content), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", c.netplanFile, err)
	}

	if err := run(ctx, "netplan", "apply"); err != nil {
		return fmt.Errorf("netplan apply: %w", err)
	}
	return nil
}

// RevertConnect restores the netplan drop-in to its state before the last
// Connect (removing it if there was none), so failed credentials don't linger and
// get re-applied later. It does not apply the change: the portal AP comes back up
// next, and leaving without connecting runs RestoreNetworking, which applies.
func (c *CoreBackend) RevertConnect(ctx context.Context) error {
	c.mu.Lock()
	prev, existed := c.prevNetplan, c.prevNetplanExists
	c.mu.Unlock()
	if existed {
		if err := os.WriteFile(c.netplanFile, prev, 0o600); err != nil {
			return fmt.Errorf("restoring %s: %w", c.netplanFile, err)
		}
		return nil
	}
	if err := os.Remove(c.netplanFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", c.netplanFile, err)
	}
	return nil
}

// classifyFor determines the security of the target SSID. It prefers the
// cached scan result; if the network isn't currently visible, it infers from the creds
// (identity => enterprise, passphrase => WPA, otherwise open).
func (c *CoreBackend) classifyFor(creds Creds) Network {
	// Use the list the user picked from rather than rescanning: a rescan here
	// would cost seconds (and, with no supplicant running, spawn one) for the same
	// answer.
	for _, n := range c.cachedNetworks() {
		if n.SSID == creds.SSID {
			return n
		}
	}
	n := Network{SSID: creds.SSID}
	switch {
	case creds.Identity != "":
		n.Security = SecurityEnterprise
	case creds.Passphrase != "":
		n.Security = SecurityWPA
	default:
		n.Security = SecurityNone
	}
	return n
}

// Connectivity polls for a usable default route until timeoutSeconds elapses.
// A reachable gateway is a good-enough signal that the client join succeeded.
func (c *CoreBackend) Connectivity(ctx context.Context, timeoutSeconds int) (bool, error) {
	deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if hasDefaultRoute(ctx, c.iface) {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}

// hasDefaultRoute reports whether there is a default route via iface, i.e. the
// WiFi interface itself joined a network and got DHCP. It must be scoped to the
// interface: on a device that is also wired (or has any other uplink) an
// unscoped check passes immediately, so a wrong password would be reported as
// success and the portal would never come back for a retry.
func hasDefaultRoute(ctx context.Context, iface string) bool {
	// `ip route show default dev X` prints nothing (exit 0) when there's none.
	cmd := exec.CommandContext(ctx, "ip", "route", "show", "default", "dev", iface)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(out) > 0
}

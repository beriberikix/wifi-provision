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
	security := c.classifyFor(ctx, creds)

	content := renderNetplan(c.iface, creds, security)

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

// classifyFor determines the security type for the target SSID. It prefers a live
// scan result; if the network isn't currently visible, it infers from the creds
// (identity => enterprise, passphrase => WPA, otherwise open).
func (c *CoreBackend) classifyFor(ctx context.Context, creds Creds) Security {
	scanCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if nets, err := c.Scan(scanCtx); err == nil {
		for _, n := range nets {
			if n.SSID == creds.SSID {
				return n.Security
			}
		}
	}
	switch {
	case creds.Identity != "":
		return SecurityEnterprise
	case creds.Passphrase != "":
		return SecurityWPA
	default:
		return SecurityNone
	}
}

// Connectivity polls for a usable default route until timeoutSeconds elapses.
// A reachable gateway is a good-enough signal that the client join succeeded.
func (c *CoreBackend) Connectivity(ctx context.Context, timeoutSeconds int) (bool, error) {
	deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if hasDefaultRoute(ctx) {
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

// hasDefaultRoute reports whether the routing table has a default route, i.e. the
// device has joined a network and can route off-link.
func hasDefaultRoute(ctx context.Context) bool {
	// `ip route show default` prints nothing (exit 0) when there's no default.
	cmd := exec.CommandContext(ctx, "ip", "route", "show", "default")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(out) > 0
}

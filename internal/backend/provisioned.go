package backend

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// WiFiProvisioned reports whether the host's netplan config already defines a
// Wi-Fi network to join (any `wifis` entry with access points, from any netplan
// file: ours, cloud-init's, Raspberry Pi Imager's...). It checks configuration,
// not connectivity, so a provisioned device whose network is briefly down still
// counts as provisioned.
func WiFiProvisioned(ctx context.Context) (bool, error) {
	// netplan merges every config file for us. Warnings (e.g. about file
	// permissions) go to stderr, so only stdout is parsed.
	out, err := exec.CommandContext(ctx, "netplan", "get", "wifis").Output()
	if err != nil {
		return false, fmt.Errorf("netplan get wifis: %w", err)
	}
	return hasAccessPoints(string(out)), nil
}

// hasAccessPoints reports whether `netplan get wifis` output configures at least
// one access point. Unconfigured, netplan prints "null".
func hasAccessPoints(netplanWifis string) bool {
	for _, line := range strings.Split(netplanWifis, "\n") {
		if strings.TrimSpace(line) == "access-points:" {
			return true
		}
	}
	return false
}

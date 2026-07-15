package backend

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
)

// CoreBackend is the NetworkBackend for default Ubuntu Core: it scans via the
// wpa_supplicant control socket, hosts the AP with hostapd + dnsmasq, and joins
// networks by writing netplan config and running `netplan apply`.
type CoreBackend struct {
	iface       string // resolved wireless interface, e.g. "wlan0"
	wpaCtrlDir  string // dir holding per-interface wpa_supplicant control sockets
	netplanFile string // path of the netplan drop-in we own

	mu sync.Mutex
	ap *apProcess // non-nil while the portal AP is up
}

// Options configure a CoreBackend. Zero values fall back to sensible defaults.
type Options struct {
	Interface   string // if empty, auto-detect the first wireless interface
	WPACtrlDir  string // default: /run/wpa_supplicant
	NetplanFile string // default: /etc/netplan/90-wifi-provision.yaml
}

const (
	defaultWPACtrlDir  = "/run/wpa_supplicant"
	defaultNetplanFile = "/etc/netplan/90-wifi-provision.yaml"
)

// NewCore builds a CoreBackend, resolving the wireless interface if not given.
func NewCore(opts Options) (*CoreBackend, error) {
	iface := opts.Interface
	if iface == "" {
		detected, err := detectWirelessInterface()
		if err != nil {
			return nil, err
		}
		iface = detected
	}

	dir := opts.WPACtrlDir
	if dir == "" {
		dir = defaultWPACtrlDir
	}
	npFile := opts.NetplanFile
	if npFile == "" {
		npFile = defaultNetplanFile
	}

	return &CoreBackend{
		iface:       iface,
		wpaCtrlDir:  dir,
		netplanFile: npFile,
	}, nil
}

// Interface returns the wireless interface the backend operates on.
func (c *CoreBackend) Interface() string { return c.iface }

// detectWirelessInterface picks the first interface that looks wireless. On Linux
// a wireless interface exposes a /sys/class/net/<name>/wireless directory (or a
// phy80211 symlink). We avoid extra dependencies by consulting sysfs directly.
func detectWirelessInterface() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("listing interfaces: %w", err)
	}
	for _, ifi := range ifaces {
		name := ifi.Name
		if isWireless(name) {
			return name, nil
		}
	}
	return "", fmt.Errorf("no wireless interface found (set --portal-interface)")
}

func isWireless(name string) bool {
	for _, sub := range []string{"wireless", "phy80211"} {
		if _, err := os.Stat(fmt.Sprintf("/sys/class/net/%s/%s", name, sub)); err == nil {
			return true
		}
	}
	// Fallback heuristic for environments without sysfs entries.
	return strings.HasPrefix(name, "wl")
}

// Package backend abstracts the network operations wifi-provision needs from the
// host: scanning for nearby WiFi, hosting the captive-portal access point, and
// joining a chosen network. The concrete implementation for default Ubuntu Core
// (wpa_supplicant control socket + hostapd/dnsmasq + netplan) lives alongside this
// file; the interface is the seam that lets tests use a fake and lets an alternate
// backend (e.g. NetworkManager) be dropped in later.
package backend

import "context"

// Security classifies an access point's authentication. The string values match
// the JSON contract the UI consumes (see GET /networks): the UI shows the
// enterprise identity field only when Security == "enterprise".
type Security string

const (
	SecurityNone       Security = "none"
	SecurityWEP        Security = "wep"
	SecurityWPA        Security = "wpa"
	SecurityEnterprise Security = "enterprise"
)

// Network is a single scan result surfaced to the portal.
type Network struct {
	SSID     string   `json:"ssid"`
	Security Security `json:"security"`
}

// Creds are the credentials a user submits via POST /connect. Identity is only
// meaningful for enterprise (802.1X) networks; Passphrase is empty for open ones.
type Creds struct {
	SSID       string `json:"ssid"`
	Identity   string `json:"identity"`
	Passphrase string `json:"passphrase"`
}

// APConfig describes the captive-portal access point to bring up.
type APConfig struct {
	Interface  string // wireless interface, e.g. "wlan0"
	SSID       string // portal SSID
	Passphrase string // WPA2 passphrase; empty means an open AP
	Gateway    string // gateway/bind IP assigned to the interface, e.g. "192.168.42.1"
	DHCPRange  string // dnsmasq range, e.g. "192.168.42.2,192.168.42.254"
}

// NetworkBackend is the full set of host network operations. The portal only
// depends on this interface, never on a concrete backend.
type NetworkBackend interface {
	// Scan returns the currently visible networks (deduped by SSID, hidden/empty
	// SSIDs dropped). It must be safe to call while the portal AP is up.
	Scan(ctx context.Context) ([]Network, error)

	// StartAP brings up the captive-portal access point and its DHCP/DNS service.
	StartAP(ctx context.Context, cfg APConfig) error

	// StopAP tears the access point (and DHCP/DNS) back down.
	StopAP(ctx context.Context) error

	// Connect persists the chosen network and joins it as a client. On default
	// Ubuntu Core this writes netplan config and runs `netplan apply`.
	Connect(ctx context.Context, creds Creds) error

	// Connectivity reports whether the device reached the wider network, polling
	// up to timeout. Used to decide success vs. retry after Connect.
	Connectivity(ctx context.Context, timeoutSeconds int) (bool, error)
}

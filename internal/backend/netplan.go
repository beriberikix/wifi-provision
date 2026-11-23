package backend

import (
	"fmt"
	"strings"
)

// renderNetplan produces a netplan config that joins the given network as a
// client on iface. It supports open, WPA/WPA2-PSK, and enterprise (802.1X PEAP)
// networks, plus WPA3-only (SAE). The output is deterministic so it can be unit-tested directly.
//
// netplan is the source of truth on default Ubuntu Core; writing this file and
// running `netplan apply` regenerates the networkd + wpa_supplicant config and
// performs the actual client join.
func renderNetplan(iface string, creds Creds, target Network, country string) string {
	var b strings.Builder
	b.WriteString("# Managed by wifi-provision. Do not edit by hand.\n")
	b.WriteString("network:\n")
	b.WriteString("  version: 2\n")
	b.WriteString("  wifis:\n")
	fmt.Fprintf(&b, "    %s:\n", iface)
	b.WriteString("      dhcp4: true\n")
	if country != "" {
		// netplan renders this both as wpa_supplicant's country= (effective on
		// its own) and as an `iw reg set` unit (only runs if iw is installed).
		fmt.Fprintf(&b, "      regulatory-domain: %s\n", yamlQuote(country))
	}
	b.WriteString("      access-points:\n")

	ssid := yamlQuote(creds.SSID)
	switch target.Security {
	case SecurityEnterprise:
		fmt.Fprintf(&b, "        %s:\n", ssid)
		b.WriteString("          auth:\n")
		b.WriteString("            key-management: eap\n")
		b.WriteString("            method: peap\n")
		fmt.Fprintf(&b, "            identity: %s\n", yamlQuote(creds.Identity))
		fmt.Fprintf(&b, "            password: %s\n", yamlQuote(creds.Passphrase))
	case SecurityNone:
		// Open network: an empty mapping so the YAML is valid without children.
		fmt.Fprintf(&b, "        %s: {}\n", ssid)
	case SecurityWPA:
		// Pin key-management. Left unset, netplan allows "WPA-PSK WPA-PSK-SHA256
		// SAE", so on a WPA2/WPA3 transition network wpa_supplicant picks SAE —
		// which the Raspberry Pi 4's brcmfmac driver fails to complete (auth
		// rejects, never associates), while plain WPA2-PSK joins fine. Use SAE
		// only when the network offers nothing else.
		km := "psk"
		if target.SAEOnly {
			km = "sae"
		}
		fmt.Fprintf(&b, "        %s:\n", ssid)
		b.WriteString("          auth:\n")
		fmt.Fprintf(&b, "            key-management: %s\n", km)
		fmt.Fprintf(&b, "            password: %s\n", yamlQuote(creds.Passphrase))
	default: // WEP: best-effort via password (netplan has no WEP key-management)
		fmt.Fprintf(&b, "        %s:\n", ssid)
		fmt.Fprintf(&b, "          password: %s\n", yamlQuote(creds.Passphrase))
	}

	return b.String()
}

// yamlQuote double-quotes a scalar and escapes backslashes and quotes, so SSIDs
// and credentials with spaces or special characters are emitted safely.
func yamlQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

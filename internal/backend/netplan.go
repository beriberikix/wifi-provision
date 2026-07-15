package backend

import (
	"fmt"
	"strings"
)

// renderNetplan produces a netplan config that joins the given network as a
// client on iface. It supports open, WPA/WPA2-PSK, and enterprise (802.1X PEAP)
// networks. The output is deterministic so it can be unit-tested directly.
//
// netplan is the source of truth on default Ubuntu Core; writing this file and
// running `netplan apply` regenerates the networkd + wpa_supplicant config and
// performs the actual client join.
func renderNetplan(iface string, creds Creds, security Security) string {
	var b strings.Builder
	b.WriteString("# Managed by wifi-provision. Do not edit by hand.\n")
	b.WriteString("network:\n")
	b.WriteString("  version: 2\n")
	b.WriteString("  wifis:\n")
	fmt.Fprintf(&b, "    %s:\n", iface)
	b.WriteString("      dhcp4: true\n")
	b.WriteString("      access-points:\n")

	ssid := yamlQuote(creds.SSID)
	switch security {
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
	default: // WPA / WPA2-PSK (and WEP, best-effort via password)
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

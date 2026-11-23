package backend

import (
	"strings"
	"testing"
)

func TestRenderNetplanWPA(t *testing.T) {
	got := renderNetplan("wlan0", Creds{SSID: "HomeNet", Passphrase: "s3cret"}, Network{Security: SecurityWPA})
	wants := []string{
		"    wlan0:\n",
		"      dhcp4: true\n",
		`        "HomeNet":` + "\n",
		"          auth:\n",
		"            key-management: psk\n",
		`            password: "s3cret"` + "\n",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("WPA netplan missing %q in:\n%s", w, got)
		}
	}
}

func TestRenderNetplanWPA3Only(t *testing.T) {
	got := renderNetplan("wlan0", Creds{SSID: "New", Passphrase: "pw"}, Network{Security: SecurityWPA, SAEOnly: true})
	if !strings.Contains(got, "            key-management: sae\n") {
		t.Errorf("WPA3-only netplan should use sae:\n%s", got)
	}
}

func TestIsSAEOnly(t *testing.T) {
	cases := map[string]bool{
		"[WPA2-SAE-CCMP][ESS]":     true,
		"[WPA2-PSK+SAE-CCMP][ESS]": false, // transition mode: use PSK
		"[WPA2-PSK-CCMP][ESS]":     false,
		"[WPA2-EAP-CCMP][ESS]":     false,
	}
	for flags, want := range cases {
		if got := isSAEOnly(flags); got != want {
			t.Errorf("isSAEOnly(%q) = %v, want %v", flags, got, want)
		}
	}
}

func TestRenderNetplanOpen(t *testing.T) {
	got := renderNetplan("wlan0", Creds{SSID: "OpenNet"}, Network{Security: SecurityNone})
	if !strings.Contains(got, `        "OpenNet": {}`+"\n") {
		t.Errorf("open netplan should use empty mapping:\n%s", got)
	}
	if strings.Contains(got, "password:") {
		t.Errorf("open netplan should have no password:\n%s", got)
	}
}

func TestRenderNetplanEnterprise(t *testing.T) {
	got := renderNetplan("wlan0", Creds{SSID: "CorpNet", Identity: "alice", Passphrase: "pw"}, Network{Security: SecurityEnterprise})
	wants := []string{
		"          auth:\n",
		"            key-management: eap\n",
		"            method: peap\n",
		`            identity: "alice"` + "\n",
		`            password: "pw"` + "\n",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("enterprise netplan missing %q in:\n%s", w, got)
		}
	}
}

func TestYamlQuoteEscaping(t *testing.T) {
	got := renderNetplan("wlan0", Creds{SSID: `My "Net" \x`, Passphrase: "p"}, Network{Security: SecurityWPA})
	if !strings.Contains(got, `"My \"Net\" \\x"`) {
		t.Errorf("SSID not escaped safely:\n%s", got)
	}
}

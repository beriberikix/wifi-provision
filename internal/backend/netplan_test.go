package backend

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestRenderNetplanWPA(t *testing.T) {
	got := renderNetplan("wlan0", Creds{SSID: "HomeNet", Passphrase: "s3cret"}, Network{Security: SecurityWPA}, "")
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
	got := renderNetplan("wlan0", Creds{SSID: "New", Passphrase: "pw"}, Network{Security: SecurityWPA, SAEOnly: true}, "")
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
	got := renderNetplan("wlan0", Creds{SSID: "OpenNet"}, Network{Security: SecurityNone}, "")
	if !strings.Contains(got, `        "OpenNet": {}`+"\n") {
		t.Errorf("open netplan should use empty mapping:\n%s", got)
	}
	if strings.Contains(got, "password:") {
		t.Errorf("open netplan should have no password:\n%s", got)
	}
}

func TestRenderNetplanEnterprise(t *testing.T) {
	got := renderNetplan("wlan0", Creds{SSID: "CorpNet", Identity: "alice", Passphrase: "pw"}, Network{Security: SecurityEnterprise}, "")
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
	got := renderNetplan("wlan0", Creds{SSID: `My "Net" \x`, Passphrase: "p"}, Network{Security: SecurityWPA}, "")
	if !strings.Contains(got, `"My \"Net\" \\x"`) {
		t.Errorf("SSID not escaped safely:\n%s", got)
	}
}

func TestRenderNetplanCountry(t *testing.T) {
	got := renderNetplan("wlan0", Creds{SSID: "N", Passphrase: "p"}, Network{Security: SecurityWPA}, "US")
	if !strings.Contains(got, `      regulatory-domain: "US"`+"\n") {
		t.Errorf("missing regulatory-domain:\n%s", got)
	}
	if strings.Contains(renderNetplan("wlan0", Creds{SSID: "N"}, Network{}, ""), "regulatory-domain") {
		t.Errorf("regulatory-domain should be omitted when country is empty")
	}
}

func TestRevertConnect(t *testing.T) {
	f := t.TempDir() + "/90-wifi-provision.yaml"

	// No drop-in before the failed join: revert removes it.
	c := &CoreBackend{netplanFile: f}
	if err := os.WriteFile(f, []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.RevertConnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatalf("drop-in should be removed, stat err = %v", err)
	}

	// A previous good drop-in: revert restores it.
	c = &CoreBackend{netplanFile: f, prevNetplan: []byte("good"), prevNetplanExists: true}
	_ = os.WriteFile(f, []byte("bad"), 0o600)
	if err := c.RevertConnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(f); string(got) != "good" {
		t.Fatalf("drop-in = %q, want restored %q", got, "good")
	}
}

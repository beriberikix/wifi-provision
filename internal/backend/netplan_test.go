package backend

import (
	"strings"
	"testing"
)

func TestRenderNetplanWPA(t *testing.T) {
	got := renderNetplan("wlan0", Creds{SSID: "HomeNet", Passphrase: "s3cret"}, SecurityWPA)
	wants := []string{
		"    wlan0:\n",
		"      dhcp4: true\n",
		`        "HomeNet":` + "\n",
		`          password: "s3cret"` + "\n",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("WPA netplan missing %q in:\n%s", w, got)
		}
	}
	if strings.Contains(got, "auth:") {
		t.Errorf("WPA netplan should not contain auth block:\n%s", got)
	}
}

func TestRenderNetplanOpen(t *testing.T) {
	got := renderNetplan("wlan0", Creds{SSID: "OpenNet"}, SecurityNone)
	if !strings.Contains(got, `        "OpenNet": {}`+"\n") {
		t.Errorf("open netplan should use empty mapping:\n%s", got)
	}
	if strings.Contains(got, "password:") {
		t.Errorf("open netplan should have no password:\n%s", got)
	}
}

func TestRenderNetplanEnterprise(t *testing.T) {
	got := renderNetplan("wlan0", Creds{SSID: "CorpNet", Identity: "alice", Passphrase: "pw"}, SecurityEnterprise)
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
	got := renderNetplan("wlan0", Creds{SSID: `My "Net" \x`, Passphrase: "p"}, SecurityWPA)
	if !strings.Contains(got, `"My \"Net\" \\x"`) {
		t.Errorf("SSID not escaped safely:\n%s", got)
	}
}

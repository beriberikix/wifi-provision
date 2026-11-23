package backend

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWaitStableDetectsEarlyExit(t *testing.T) {
	alive, err := startChild("sleeper", exec.Command("sleep", "5"))
	if err != nil {
		t.Fatal(err)
	}
	defer alive.stop()
	dead, err := startChild("quitter", exec.Command("false"))
	if err != nil {
		t.Fatal(err)
	}
	err = waitStable(time.Second, alive, dead)
	if err == nil || !strings.Contains(err.Error(), "quitter exited") {
		t.Fatalf("want quitter exit error, got %v", err)
	}
}

func TestWaitStableAllUp(t *testing.T) {
	alive, err := startChild("sleeper", exec.Command("sleep", "5"))
	if err != nil {
		t.Fatal(err)
	}
	defer alive.stop()
	if err := waitStable(200*time.Millisecond, alive); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestDnsmasqArgsSnap(t *testing.T) {
	cfg := APConfig{Gateway: "192.168.42.1", DHCPRange: "192.168.42.2,192.168.42.254"}

	t.Setenv("SNAP", "")
	if slices.Contains(dnsmasqArgs("wlan0", cfg), "--user=root") {
		t.Error("outside a snap dnsmasq should drop privileges")
	}

	t.Setenv("SNAP", "/snap/wifi-provision/x1")
	args := dnsmasqArgs("wlan0", cfg)
	for _, want := range []string{"--user=root", "--group=", "--interface=wlan0"} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
}

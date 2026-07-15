package config

import "testing"

// envFunc builds a getenv from a map for deterministic tests.
func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	cfg, err := Load(nil, envFunc(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SSID != DefaultSSID {
		t.Errorf("SSID = %q, want %q", cfg.SSID, DefaultSSID)
	}
	if cfg.Gateway != DefaultGateway {
		t.Errorf("Gateway = %q, want %q", cfg.Gateway, DefaultGateway)
	}
	if cfg.ListeningPort != DefaultListeningPort {
		t.Errorf("ListeningPort = %d, want %d", cfg.ListeningPort, DefaultListeningPort)
	}
	if cfg.Interface != "" {
		t.Errorf("Interface = %q, want empty", cfg.Interface)
	}
}

func TestEnvOverridesDefault(t *testing.T) {
	env := map[string]string{
		"PORTAL_SSID":           "MyPortal",
		"PORTAL_LISTENING_PORT": "8080",
		"ACTIVITY_TIMEOUT":      "300",
	}
	cfg, err := Load(nil, envFunc(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SSID != "MyPortal" {
		t.Errorf("SSID = %q, want MyPortal", cfg.SSID)
	}
	if cfg.ListeningPort != 8080 {
		t.Errorf("ListeningPort = %d, want 8080", cfg.ListeningPort)
	}
	if cfg.ActivityTimeout != 300 {
		t.Errorf("ActivityTimeout = %d, want 300", cfg.ActivityTimeout)
	}
}

func TestFlagOverridesEnv(t *testing.T) {
	env := map[string]string{"PORTAL_SSID": "FromEnv"}
	cfg, err := Load([]string{"--portal-ssid", "FromFlag"}, envFunc(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SSID != "FromFlag" {
		t.Errorf("SSID = %q, want FromFlag (flag beats env)", cfg.SSID)
	}
}

func TestShortFlagAlias(t *testing.T) {
	cfg, err := Load([]string{"-s", "Short", "-o", "9090"}, envFunc(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SSID != "Short" {
		t.Errorf("SSID = %q, want Short", cfg.SSID)
	}
	if cfg.ListeningPort != 9090 {
		t.Errorf("ListeningPort = %d, want 9090", cfg.ListeningPort)
	}
}

func TestInvalidInputs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"bad port", []string{"--portal-listening-port", "notaport"}, nil},
		{"port out of range", []string{"--portal-listening-port", "70000"}, nil},
		{"negative timeout", []string{"--activity-timeout", "-5"}, nil},
		{"bad gateway", []string{"--portal-gateway", "not.an.ip"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(tc.args, envFunc(tc.env)); err == nil {
				t.Errorf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

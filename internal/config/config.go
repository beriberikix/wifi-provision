// Package config parses wifi-provision's runtime configuration. Precedence for
// every option is: command-line flag > environment variable > built-in default,
// matching the original wifi-connect surface so operators' muscle memory carries
// over. The UI is embedded in the binary, so the source's --ui-directory option
// is intentionally dropped.
package config

import (
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
)

// Defaults mirror the original wifi-connect (src/config.rs).
const (
	DefaultSSID            = "WiFi Connect"
	DefaultGateway         = "192.168.42.1"
	DefaultDHCPRange       = "192.168.42.2,192.168.42.254"
	DefaultListeningPort   = 80
	DefaultActivityTimeout = 0 // seconds; 0 disables the timeout
)

// Config is the fully-resolved configuration.
type Config struct {
	Interface       string // empty => auto-detect the first wireless interface
	SSID            string
	Passphrase      string // empty => open portal AP
	Gateway         string
	DHCPRange       string
	ListeningPort   int
	ActivityTimeout int // seconds; 0 disables
}

// Load resolves configuration from the given args (excluding the program name)
// and the process environment. It returns an error rather than exiting so callers
// and tests stay in control.
func Load(args []string, getenv func(string) string) (Config, error) {
	fs := flag.NewFlagSet("wifi-provision", flag.ContinueOnError)

	// Flags default to empty/sentinel so we can tell "unset" from "set to default"
	// and apply the flag > env > default precedence ourselves.
	var (
		iface   = fs.String("portal-interface", "", "wireless interface to use (default: auto-detect)")
		ssid    = fs.String("portal-ssid", "", fmt.Sprintf("captive-portal SSID (default: %q)", DefaultSSID))
		pass    = fs.String("portal-passphrase", "", "captive-portal WPA2 passphrase (default: open)")
		gateway = fs.String("portal-gateway", "", fmt.Sprintf("captive-portal gateway IP (default: %s)", DefaultGateway))
		dhcp    = fs.String("portal-dhcp-range", "", fmt.Sprintf("DHCP range (default: %s)", DefaultDHCPRange))
		port    = fs.String("portal-listening-port", "", fmt.Sprintf("HTTP port (default: %d)", DefaultListeningPort))
		timeout = fs.String("activity-timeout", "", "exit if portal unused for N seconds (default: 0, disabled)")
	)
	// Short aliases matching the original tool.
	fs.StringVar(iface, "i", "", "alias for --portal-interface")
	fs.StringVar(ssid, "s", "", "alias for --portal-ssid")
	fs.StringVar(pass, "p", "", "alias for --portal-passphrase")
	fs.StringVar(gateway, "g", "", "alias for --portal-gateway")
	fs.StringVar(dhcp, "d", "", "alias for --portal-dhcp-range")
	fs.StringVar(port, "o", "", "alias for --portal-listening-port")
	fs.StringVar(timeout, "a", "", "alias for --activity-timeout")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	pick := func(flagVal, envKey, def string) string {
		if flagVal != "" {
			return flagVal
		}
		if v := getenv(envKey); v != "" {
			return v
		}
		return def
	}

	cfg := Config{
		Interface:  pick(*iface, "PORTAL_INTERFACE", ""),
		SSID:       pick(*ssid, "PORTAL_SSID", DefaultSSID),
		Passphrase: pick(*pass, "PORTAL_PASSPHRASE", ""),
		Gateway:    pick(*gateway, "PORTAL_GATEWAY", DefaultGateway),
		DHCPRange:  pick(*dhcp, "PORTAL_DHCP_RANGE", DefaultDHCPRange),
	}

	portStr := pick(*port, "PORTAL_LISTENING_PORT", strconv.Itoa(DefaultListeningPort))
	p, err := strconv.Atoi(portStr)
	if err != nil || p < 1 || p > 65535 {
		return Config{}, fmt.Errorf("invalid listening port %q", portStr)
	}
	cfg.ListeningPort = p

	toStr := pick(*timeout, "ACTIVITY_TIMEOUT", strconv.Itoa(DefaultActivityTimeout))
	t, err := strconv.Atoi(toStr)
	if err != nil || t < 0 {
		return Config{}, fmt.Errorf("invalid activity timeout %q", toStr)
	}
	cfg.ActivityTimeout = t

	if net.ParseIP(cfg.Gateway) == nil {
		return Config{}, fmt.Errorf("invalid gateway address %q", cfg.Gateway)
	}

	return cfg, nil
}

// LoadOrExit is a convenience wrapper for main: it loads from os.Args/os.Environ
// and exits with a message on error.
func LoadOrExit() Config {
	cfg, err := Load(os.Args[1:], os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wifi-provision: %v\n", err)
		os.Exit(2)
	}
	return cfg
}

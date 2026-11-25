package backend

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Scan returns the visible networks. A single radio cannot scan as a client while
// it is hosting the portal AP, so while the AP is up Scan returns the results
// captured just before the AP started; otherwise it performs a live scan and
// refreshes that cache.
func (c *CoreBackend) Scan(ctx context.Context) ([]Network, error) {
	c.mu.Lock()
	apUp := c.ap != nil
	cached := c.cached
	c.mu.Unlock()
	if apUp {
		if cached == nil {
			return []Network{}, nil
		}
		return cached, nil
	}

	nets, err := c.liveScan(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cached = nets
	c.mu.Unlock()
	return nets, nil
}

// cachedNetworks returns the last scan results without touching the radio.
func (c *CoreBackend) cachedNetworks() []Network {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cached
}

// liveScan scans through wpa_supplicant. If one is already managing the interface
// (e.g. netplan's netplan-wpa-<iface>.service) its control socket is used;
// otherwise — the normal case on a device with no WiFi configured yet — a
// short-lived private wpa_supplicant is started just for the scan.
func (c *CoreBackend) liveScan(ctx context.Context) ([]Network, error) {
	sockPath := filepath.Join(c.wpaCtrlDir, c.iface)
	if _, err := os.Stat(sockPath); err == nil {
		return scanVia(ctx, sockPath)
	}

	tmp, err := startTempSupplicant(ctx, c.iface)
	if err != nil {
		return nil, err
	}
	defer tmp.stop()
	return scanVia(ctx, tmp.sockPath)
}

// tempSupplicant is a private wpa_supplicant instance used only for scanning.
type tempSupplicant struct {
	cmd      *exec.Cmd
	dir      string
	sockPath string
}

func startTempSupplicant(ctx context.Context, iface string) (*tempSupplicant, error) {
	dir, err := os.MkdirTemp("", "wifi-provision-wpa-")
	if err != nil {
		return nil, fmt.Errorf("creating wpa_supplicant temp dir: %w", err)
	}
	conf := filepath.Join(dir, "scan.conf")
	if err := os.WriteFile(conf, []byte("ctrl_interface="+dir+"\n"), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("writing wpa_supplicant scan config: %w", err)
	}

	cmd := exec.Command("wpa_supplicant", "-i", iface, "-c", conf, "-D", "nl80211,wext")
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("starting wpa_supplicant for scanning: %w", err)
	}
	t := &tempSupplicant{cmd: cmd, dir: dir, sockPath: filepath.Join(dir, iface)}

	// Wait for the control socket to appear.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(t.sockPath); err == nil {
			return t, nil
		}
		if time.Now().After(deadline) {
			t.stop()
			return nil, fmt.Errorf("wpa_supplicant control socket %s did not appear", t.sockPath)
		}
		select {
		case <-ctx.Done():
			t.stop()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (t *tempSupplicant) stop() {
	if cl, err := dialWPA(t.sockPath); err == nil {
		_, _ = cl.cmd("TERMINATE")
		cl.close()
	}
	done := make(chan struct{})
	go func() { _ = t.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = t.cmd.Process.Kill()
		<-done
	}
	_ = os.RemoveAll(t.dir)
}

// releaseSupplicant terminates any wpa_supplicant managing the interface so it
// cannot pull the radio back into station mode underneath hostapd. It reports
// whether one was running. netplan's netplan-wpa-<iface>.service uses Restart=no,
// so it stays down until the next `netplan apply`.
func (c *CoreBackend) releaseSupplicant() (bool, error) {
	sockPath := filepath.Join(c.wpaCtrlDir, c.iface)
	if _, err := os.Stat(sockPath); err != nil {
		return false, nil
	}
	cl, err := dialWPA(sockPath)
	if err != nil {
		// Only a stale socket with nobody listening means there is nothing to
		// release. Any other failure (e.g. a confinement denial) must stop
		// StartAP: carrying on would start hostapd with the supplicant still
		// fighting it for the radio.
		if errors.Is(err, syscall.ECONNREFUSED) {
			return false, nil
		}
		return false, fmt.Errorf("contacting wpa_supplicant on %s to stop it: %w", c.iface, err)
	}
	_, err = cl.cmd("TERMINATE")
	cl.close()
	if err != nil {
		return true, fmt.Errorf("terminating wpa_supplicant on %s: %w", c.iface, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err != nil {
			return true, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return true, fmt.Errorf("wpa_supplicant on %s did not exit", c.iface)
}

// scanVia drives a wpa_supplicant control socket: request a scan, wait for it to
// finish, then read and parse SCAN_RESULTS. It ATTACHes to the socket so it can
// wait for CTRL-EVENT-SCAN-RESULTS rather than guessing: a radio that is already
// associated scans channels between data frames and can take several seconds,
// and reading early returns only the BSS it is connected to. If ATTACH isn't
// available it falls back to a fixed wait. A few attempts cover FAIL-BUSY and a
// freshly started supplicant that hasn't brought the radio up yet.
func scanVia(ctx context.Context, sockPath string) ([]Network, error) {
	cl, err := dialWPA(sockPath)
	if err != nil {
		return nil, fmt.Errorf("connecting to wpa_supplicant control socket %s: %w", sockPath, err)
	}
	defer cl.close()

	attached := false
	if resp, err := cl.cmd("ATTACH"); err == nil && strings.HasPrefix(resp, "OK") {
		attached = true
		defer func() { _, _ = cl.cmd("DETACH") }()
	}

	const attempts = 3
	best := []Network{}
	for range attempts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// FAIL-BUSY means a scan is already running; its results event still comes.
		_, _ = cl.cmd("SCAN")

		completed := false
		if attached {
			completed, err = cl.waitEvent(ctx, "CTRL-EVENT-SCAN-RESULTS", 15*time.Second)
			if err != nil {
				return nil, err
			}
		} else {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(3 * time.Second):
			}
		}

		resp, err := cl.cmd("SCAN_RESULTS")
		if err != nil {
			return nil, fmt.Errorf("reading scan results: %w", err)
		}
		if nets := parseScanResults(resp); len(nets) > len(best) {
			best = nets
		}
		if completed && len(best) > 0 {
			return best, nil
		}
	}
	// No networks found after retries is not an error — return what we have.
	return best, nil
}

// parseScanResults parses wpa_supplicant SCAN_RESULTS output. The format is a
// header line followed by tab-separated rows:
//
//	bssid / frequency / signal level / flags / ssid
//
// Rows are deduped by SSID (first seen wins) and hidden/empty SSIDs are dropped.
func parseScanResults(out string) []Network {
	lines := strings.Split(out, "\n")
	seen := make(map[string]bool)
	var nets []Network

	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "bssid ") {
			continue
		}
		// SSID may itself contain no tabs (wpa escapes control chars), so splitting
		// on tab into 5 fields is safe; anything past the 5th field is still SSID.
		fields := strings.SplitN(line, "\t", 5)
		if len(fields) < 5 {
			continue
		}
		flags := fields[3]
		ssid := fields[4]
		if ssid == "" || seen[ssid] {
			continue
		}
		seen[ssid] = true
		nets = append(nets, Network{
			SSID:     ssid,
			Security: classifySecurity(flags),
			SAEOnly:  isSAEOnly(flags),
		})
	}
	if nets == nil {
		return []Network{}
	}
	return nets
}

// classifySecurity maps a wpa_supplicant flags string (e.g.
// "[WPA2-PSK-CCMP][ESS]" or "[WPA2-EAP-CCMP][ESS]") to the security category the
// UI expects. Order matters: enterprise (EAP) before PSK before WEP before none.
func classifySecurity(flags string) Security {
	f := strings.ToUpper(flags)
	switch {
	case strings.Contains(f, "EAP"):
		return SecurityEnterprise
	case strings.Contains(f, "PSK"), strings.Contains(f, "WPA"):
		return SecurityWPA
	case strings.Contains(f, "WEP"):
		return SecurityWEP
	default:
		return SecurityNone
	}
}

// isSAEOnly reports a WPA3-only network: SAE offered with no PSK fallback.
// Transition-mode networks ("[WPA2-PSK+SAE-CCMP]") are not SAE-only.
func isSAEOnly(flags string) bool {
	f := strings.ToUpper(flags)
	return strings.Contains(f, "SAE") && !strings.Contains(f, "PSK")
}

// wpaClient is a minimal wpa_supplicant control-interface client over a unix
// datagram socket. wpa_supplicant replies to the client's bound address, so the
// client must bind one.
type wpaClient struct {
	conn   *net.UnixConn
	events []string // unsolicited events read while waiting for a reply
}

func dialWPA(remotePath string) (*wpaClient, error) {
	// Bind in the abstract namespace ("@" prefix) rather than a /tmp path, as
	// wpa_cli does. wpa_supplicant resolves the reply address in its own mount
	// namespace, and under snap confinement our /tmp is private: replies to a
	// /tmp path never arrive. Abstract sockets have no filesystem path, so they
	// work across mount namespaces and leave nothing to clean up.
	local := fmt.Sprintf("%swpa-%d-%d", abstractPrefix(), os.Getpid(), time.Now().UnixNano())
	laddr := &net.UnixAddr{Name: local, Net: "unixgram"}
	raddr := &net.UnixAddr{Name: remotePath, Net: "unixgram"}

	conn, err := net.DialUnix("unixgram", laddr, raddr)
	if err != nil {
		return nil, err
	}
	return &wpaClient{conn: conn}, nil
}

func (w *wpaClient) cmd(command string) (string, error) {
	if err := w.conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return "", err
	}
	if _, err := w.conn.Write([]byte(command)); err != nil {
		return "", err
	}
	// Each reply is one datagram. Once ATTACHed, unsolicited events ("<N>...")
	// can arrive ahead of it; keep them for waitEvent and read on.
	buf := make([]byte, 64*1024)
	for {
		n, err := w.conn.Read(buf)
		if err != nil {
			return "", err
		}
		msg := string(buf[:n])
		if isEvent(msg) {
			w.events = append(w.events, msg)
			continue
		}
		return msg, nil
	}
}

// waitEvent blocks until an unsolicited event containing name arrives (true),
// the timeout passes (false), or ctx is done.
func (w *wpaClient) waitEvent(ctx context.Context, name string, timeout time.Duration) (bool, error) {
	for i, ev := range w.events {
		if strings.Contains(ev, name) {
			w.events = w.events[i+1:]
			return true, nil
		}
	}
	w.events = nil

	deadline := time.Now().Add(timeout)
	buf := make([]byte, 64*1024)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		// Short reads so ctx cancellation is noticed promptly.
		next := time.Now().Add(500 * time.Millisecond)
		if next.After(deadline) {
			next = deadline
		}
		if err := w.conn.SetReadDeadline(next); err != nil {
			return false, err
		}
		n, err := w.conn.Read(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return false, err
		}
		if msg := string(buf[:n]); isEvent(msg) && strings.Contains(msg, name) {
			return true, nil
		}
	}
	return false, nil
}

// isEvent reports whether a control-socket datagram is an unsolicited event,
// which wpa_supplicant prefixes with a priority like "<3>".
func isEvent(msg string) bool {
	return len(msg) > 2 && msg[0] == '<' && msg[1] >= '0' && msg[1] <= '9'
}

// abstractPrefix returns the abstract-socket name prefix. snapd's AppArmor
// profile only permits binding abstract names under "@snap.<instance-name>.", so
// use that when confined.
func abstractPrefix() string {
	if name := os.Getenv("SNAP_INSTANCE_NAME"); name != "" {
		return "@snap." + name + "."
	}
	return "@wifi-provision-"
}

func (w *wpaClient) close() {
	_ = w.conn.Close()
}

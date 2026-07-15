package backend

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Scan drives the wpa_supplicant control interface: request a scan, wait briefly
// for results to populate, then read and parse SCAN_RESULTS. It retries a few
// times because results can be empty immediately after an AP teardown (the
// original wifi-connect retried up to 10x).
func (c *CoreBackend) Scan(ctx context.Context) ([]Network, error) {
	sockPath := filepath.Join(c.wpaCtrlDir, c.iface)

	const retries = 10
	for range retries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		cl, err := dialWPA(sockPath)
		if err != nil {
			return nil, fmt.Errorf("connecting to wpa_supplicant control socket %s: %w", sockPath, err)
		}

		// SCAN may return FAIL-BUSY if a scan is already running; that's fine, we
		// still read whatever results are available.
		_, _ = cl.cmd("SCAN")

		// Give the scan a moment to complete before reading results.
		select {
		case <-ctx.Done():
			cl.close()
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}

		resp, err := cl.cmd("SCAN_RESULTS")
		cl.close()
		if err != nil {
			return nil, fmt.Errorf("reading scan results: %w", err)
		}

		nets := parseScanResults(resp)
		if len(nets) > 0 {
			return nets, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	// No networks found after retries is not an error — return an empty list.
	return []Network{}, nil
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

// wpaClient is a minimal wpa_supplicant control-interface client over a unix
// datagram socket. wpa_supplicant replies to the client's bound socket, so we use
// an autobind abstract/local address.
type wpaClient struct {
	conn      *net.UnixConn
	localPath string
}

func dialWPA(remotePath string) (*wpaClient, error) {
	// wpa_supplicant control sockets are SOCK_DGRAM unix sockets. The client must
	// bind its own local socket to receive replies.
	local := filepath.Join(os.TempDir(), fmt.Sprintf("wpa_ctrl_%d_%d", os.Getpid(), time.Now().UnixNano()))
	laddr := &net.UnixAddr{Name: local, Net: "unixgram"}
	raddr := &net.UnixAddr{Name: remotePath, Net: "unixgram"}

	conn, err := net.DialUnix("unixgram", laddr, raddr)
	if err != nil {
		return nil, err
	}
	return &wpaClient{conn: conn, localPath: local}, nil
}

func (w *wpaClient) cmd(command string) (string, error) {
	if err := w.conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return "", err
	}
	if _, err := w.conn.Write([]byte(command)); err != nil {
		return "", err
	}
	buf := make([]byte, 8192)
	var sb strings.Builder
	// A single reply fits in one datagram for our commands; read once, then drain
	// any immediately-available continuation without blocking long.
	n, err := w.conn.Read(buf)
	if err != nil {
		return "", err
	}
	sb.Write(buf[:n])
	return sb.String(), nil
}

func (w *wpaClient) close() {
	_ = w.conn.Close()
	_ = os.Remove(w.localPath)
}

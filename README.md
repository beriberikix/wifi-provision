# wifi-provision

Easy WiFi setup for headless Linux devices from your phone or laptop — built for
**default Ubuntu Core**.

wifi-provision hosts a temporary WiFi access point with a captive portal. Connect
to it from a phone, pick your network, enter the password, and the device joins it
and remembers it across reboots. It targets the Core-native networking stack
(**netplan → systemd-networkd → wpa_supplicant**) and requires **no NetworkManager**.

It's a from-scratch Go reimplementation of the UX pioneered by
[balena wifi-connect](https://github.com/balena-io/wifi-connect), whose hard
dependency on NetworkManager makes it unusable on a stock Ubuntu Core image.

## How it works

1. **Advertise** — brings up an access point (`hostapd`) with DHCP + captive-portal
   DNS (`dnsmasq`), both bundled in the snap.
2. **Portal** — serves a small embedded web UI. `GET /networks` scans via the
   wpa_supplicant control socket; `POST /connect` submits the chosen credentials.
3. **Connect** — tears the AP down, writes `/etc/netplan/90-wifi-provision.yaml`,
   and runs `netplan apply`, which joins the network as a client. Persists across
   reboots. On failure the portal comes back up for another try.

Supports open, WPA/WPA2-PSK, and enterprise (802.1X PEAP) networks. Single-shot:
the process exits once the device is connected.

## Configuration

Precedence: **flag > environment variable > default**.

| Flag | Env | Default | Purpose |
|---|---|---|---|
| `--portal-interface`, `-i` | `PORTAL_INTERFACE` | auto-detect | Wireless interface |
| `--portal-ssid`, `-s` | `PORTAL_SSID` | `WiFi Connect` | Portal AP SSID |
| `--portal-passphrase`, `-p` | `PORTAL_PASSPHRASE` | (open) | Portal AP WPA2 passphrase |
| `--portal-gateway`, `-g` | `PORTAL_GATEWAY` | `192.168.42.1` | AP gateway / bind IP |
| `--portal-dhcp-range`, `-d` | `PORTAL_DHCP_RANGE` | `192.168.42.2,192.168.42.254` | DHCP range |
| `--portal-listening-port`, `-o` | `PORTAL_LISTENING_PORT` | `80` | HTTP port |
| `--activity-timeout`, `-a` | `ACTIVITY_TIMEOUT` | `0` (off) | Exit if portal unused for N seconds |

## Build & test (dev machine)

```sh
go build ./...
go vet ./...
go test ./...
```

The web UI is embedded in the binary via `go:embed` — no Node/npm toolchain.

## Package as a snap

```sh
snapcraft
sudo snap install --dangerous ./wifi-provision_0.1.0_*.snap
sudo snap connect wifi-provision:network-control
sudo snap connect wifi-provision:network-setup-control
sudo snap connect wifi-provision:firewall-control
sudo snap start wifi-provision
```

## Testing in multipass

A multipass Ubuntu Core VM is the primary on-Core dev/CI environment for everything
**except** live RF — a VM has no wireless radio, so real scan/AP/client-join can't
run there. Use it to validate snap install, plug connections under strict
confinement, netplan file writing, and the portal/config surface. Final RF sign-off
happens on real hardware (e.g. a Raspberry Pi on Ubuntu Core).

```sh
multipass launch core24 --name core-dev
# transfer & install the snap, connect plugs, exercise the portal
```

## Architecture

```
cmd/wifi-provision   orchestrator: lifecycle, activity timeout, signal handling
internal/config      flag+env+default parsing
internal/portal      HTTP server, JSON handlers, captive-portal redirect, embedded UI
internal/backend     NetworkBackend interface + Ubuntu Core implementation
  core.go            interface detection, backend wiring
  scan.go            wpa_supplicant control-socket client + result parsing
  ap.go              hostapd + dnsmasq supervision
  netplan.go         netplan YAML rendering
  connect.go         netplan apply + connectivity check
```

`NetworkBackend` is the seam: the portal depends only on the interface, so tests
use a fake and an alternate backend (e.g. NetworkManager) could be added later.

## License

TBD.

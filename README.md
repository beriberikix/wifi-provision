# wifi-provision

Easy WiFi setup for headless Linux devices from your phone or laptop — built for
**Ubuntu Core and Ubuntu Server**.

wifi-provision hosts a temporary WiFi access point with a captive portal. Connect
to it from a phone, pick your network, enter the password, and the device joins it
and remembers it across reboots. It targets the networking stack that Core and
Server share (**netplan → systemd-networkd → wpa_supplicant**) and requires **no
NetworkManager**.

It's a from-scratch Go reimplementation of the UX pioneered by
[balena wifi-connect](https://github.com/balena-io/wifi-connect), whose hard
dependency on NetworkManager makes it unusable on a stock Ubuntu Core image.

## Tested on

| Platform | Hardware | How | Result |
|---|---|---|---|
| Ubuntu Server 26.10 (arm64) | Raspberry Pi 4 | binary + apt `hostapd`/`dnsmasq-base` | Full flow from a phone; survives reboot |
| Ubuntu Server 26.10 (arm64) | Raspberry Pi 4 | strict snap (core24) | Full flow, no AppArmor denials |

Ubuntu Core uses the same stack and the same snap, but hasn't yet been tested on
real hardware.

## How it works

1. **Scan** — scans for nearby networks *before* the AP starts, because a single
   radio can't scan as a client while it's hosting an AP. It uses the
   wpa_supplicant already managing the interface if there is one; otherwise (a
   fresh device) it starts a short-lived private wpa_supplicant just to scan.
2. **Advertise** — stops any wpa_supplicant on the interface (netplan's
   `netplan-wpa-<iface>.service`; left running, it pulls the radio back into
   client mode and the AP never broadcasts), then brings up an access point
   (`hostapd`) with DHCP + captive-portal DNS (`dnsmasq`).
3. **Portal** — serves a small embedded web UI. `GET /networks` returns the
   list from step 1; `POST /connect` submits the chosen credentials.
4. **Connect** — tears the AP down, writes `/etc/netplan/90-wifi-provision.yaml`,
   and runs `netplan apply`, which joins the network as a client and persists
   across reboots. Success means a default route *via the WiFi interface* within
   45s. On failure the drop-in is reverted (so bad credentials aren't kept) and
   the portal comes back for another try.

Supports open, WPA2/WPA3-transition (joined as WPA2-PSK), WPA3-only (SAE), and
enterprise (802.1X PEAP) networks. Single-shot: the process exits once the device
is connected. If it exits without connecting (timeout, signal, error) it hands
the radio back to the host's own networking.

### Exit codes

| Code | Meaning |
|---|---|
| `0` | Connected — or stopped by SIGINT/SIGTERM (e.g. `snap stop`) |
| `1` | Unrecoverable error (e.g. the AP couldn't start) |
| `2` | Invalid configuration |
| `3` | `--activity-timeout` passed with no portal activity |

## Configuration

Precedence: **flag > environment variable > default**. In the snap, set options
with `snap set` (next section).

| Flag | Env | `snap set` key | Default | Purpose |
|---|---|---|---|---|
| `--portal-interface`, `-i` | `PORTAL_INTERFACE` | `interface` | auto-detect | Wireless interface |
| `--portal-ssid`, `-s` | `PORTAL_SSID` | `ssid` | `WiFi Connect` | Portal AP SSID |
| `--portal-passphrase`, `-p` | `PORTAL_PASSPHRASE` | `passphrase` | (open) | Portal AP WPA2 passphrase |
| `--portal-gateway`, `-g` | `PORTAL_GATEWAY` | `gateway` | `192.168.42.1` | AP gateway / bind IP |
| `--portal-dhcp-range`, `-d` | `PORTAL_DHCP_RANGE` | `dhcp-range` | `192.168.42.2,192.168.42.254` | DHCP range |
| `--portal-listening-port`, `-o` | `PORTAL_LISTENING_PORT` | `listening-port` | `80` | HTTP port |
| `--activity-timeout`, `-a` | `ACTIVITY_TIMEOUT` | `activity-timeout` | `0` (off) | Exit if portal unused for N seconds |
| `--country`, `-c` | `PORTAL_COUNTRY` | `country` | (unset) | WiFi regulatory domain, e.g. `US` |

### Regulatory domain (`--country`)

Set this to the country the device operates in. It's applied two ways, neither of
which needs `iw`:

- the portal AP advertises it (hostapd `country_code` + 802.11d);
- the saved netplan config gets `regulatory-domain`, which netplan passes to
  wpa_supplicant as `country=` for every later client connection.

netplan also renders `regulatory-domain` as an `iw reg set` unit, but neither
Ubuntu Core nor a stock Ubuntu Server ships `iw`, so that unit is skipped. Some
radios — including the Raspberry Pi 4's (brcmfmac) — manage their regulatory
domain in firmware and ignore it either way.

## Install

### As a snap (Ubuntu Core or Server)

```sh
snapcraft pack
sudo snap install --dangerous ./wifi-provision_0.1.0_*.snap
sudo snap connect wifi-provision:network-control
sudo snap connect wifi-provision:network-setup-control
sudo snap connect wifi-provision:firewall-control
sudo snap set wifi-provision country=US      # optional; see Configuration
sudo snap start wifi-provision
```

The service is installed disabled and runs once per `snap start`. It isn't
restarted after a deliberate exit (connected, or activity timeout), only after a
crash. Logs: `sudo snap logs -n 50 wifi-provision`.

To build for arm64 (e.g. a Raspberry Pi), build on an arm64 machine; the Pi itself
works (`sudo snap install snapcraft --classic`, `sudo snap install lxd`,
`sudo lxd init --auto`).

### Without a snap (Ubuntu Server)

```sh
sudo apt install hostapd dnsmasq-base     # wpasupplicant + netplan ship with Server
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o wifi-provision ./cmd/wifi-provision
sudo ./wifi-provision --country US
```

The apt `hostapd` service is masked by default; wifi-provision runs its own
instance and doesn't need it enabled.

## Platform notes

- **Raspberry Pi 4 and WPA3:** the Pi 4's brcmfmac driver fails SAE (WPA3)
  authentication. On a WPA2/WPA3 transition network, netplan's default
  key-management lets wpa_supplicant pick SAE and the join never completes, so
  wifi-provision pins `key-management: psk` for any network that offers WPA2.
  WPA3-only networks get `sae`, which this hardware may not be able to join.
- **Existing WiFi config:** if another netplan file (e.g. cloud-init's
  `50-cloud-init.yaml`, or Raspberry Pi Imager's settings) already configures
  the same interface, netplan merges it with `90-wifi-provision.yaml` and the
  device will know both networks.
- **Ethernet:** a wired connection doesn't confuse the success check (it requires
  a route via the WiFi interface), and keeps SSH up while the AP owns the radio —
  the easiest way to develop on real hardware.

## Build & test (dev machine)

```sh
go build ./...
go vet ./...
go test ./...
go run ./cmd/portal-dev        # portal UI against a fake backend, on 127.0.0.1:8080
```

The web UI is embedded in the binary via `go:embed` — no Node/npm toolchain.

## Testing in multipass

A multipass Ubuntu Core VM is useful for everything **except** live RF — a VM has
no wireless radio, so real scan/AP/client-join can't run there. Use it to
validate snap install, plug connections under strict confinement, netplan file
writing, and the portal/config surface. RF sign-off happens on real hardware
(e.g. a Raspberry Pi, with Ethernet attached so you keep SSH).

```sh
multipass launch core24 --name core-dev
# transfer & install the snap, connect plugs, exercise the portal
```

## Architecture

```
cmd/wifi-provision   orchestrator: lifecycle, outcomes/exit codes, timeout, signals
cmd/portal-dev       portal UI against a fake backend, for local development
internal/config      flag+env+default parsing
internal/portal      HTTP server, JSON handlers, captive-portal redirect, embedded UI
internal/backend     NetworkBackend interface + netplan/networkd implementation
  core.go            interface detection, backend wiring
  scan.go            pre-AP scan + cache, temp/existing wpa_supplicant, control-socket client
  ap.go              releasing the radio, hostapd + dnsmasq supervision, restore
  netplan.go         netplan YAML rendering
  connect.go         netplan apply, revert on failure, connectivity check
snap/                snapcraft.yaml, launcher (snap set → env), configure hook
```

`NetworkBackend` is the seam: the portal depends only on the interface, so tests
use a fake and an alternate backend (e.g. NetworkManager) could be added later.

## License

Apache License 2.0 — see [LICENSE](LICENSE).

Maintainer: Jonathan Beri <jmberi@gmail.com>

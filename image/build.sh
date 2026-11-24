#!/usr/bin/env bash
# Build a headless Ubuntu Core 26 image for Raspberry Pi with a seeded SSH user.
#
# The image is Canonical's stock ubuntu-core-26-pi-arm64 model with three
# changes: your brand, grade dangerous (required to seed a system user), and no
# console-conf. It contains only the stock snaps; install wifi-provision on it
# afterwards.
#
# Requirements:
#   - ubuntu-image:  sudo snap install ubuntu-image --classic
#   - a signing key registered to your Snap Store account:
#       snapcraft login; snapcraft create-key <name>; snapcraft register-key <name>
#
# Usage (all variables except USER_* have defaults):
#   USER_NAME="Jane Doe" USER_USERNAME=jane USER_EMAIL=jane@example.com \
#     image/build.sh
#
#   SNAP_KEY         signing key name                (default: wifi-provision)
#   BRAND_ID         store account ID owning the key (default: from model.json)
#   SSH_PUBKEY_FILE  key the user logs in with       (default: ~/.ssh/id_ed25519.pub)
#   VALID_DAYS       system-user validity            (default: 365)
#
# Output: image/out/pi.img and image/out/pi.img.xz. Flash with Raspberry Pi
# Imager ("Use custom"), boot on Ethernet, then: ssh $USER_USERNAME@<ip>
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
out=$here/out

: "${USER_NAME:?set USER_NAME (full name)}"
: "${USER_USERNAME:?set USER_USERNAME (login name)}"
: "${USER_EMAIL:?set USER_EMAIL}"
SNAP_KEY=${SNAP_KEY:-wifi-provision}
SSH_PUBKEY_FILE=${SSH_PUBKEY_FILE:-$HOME/.ssh/id_ed25519.pub}
VALID_DAYS=${VALID_DAYS:-365}
export USER_NAME USER_USERNAME USER_EMAIL SSH_PUBKEY_FILE VALID_DAYS BRAND_ID="${BRAND_ID:-}"

command -v ubuntu-image >/dev/null || { echo "ubuntu-image not found: sudo snap install ubuntu-image --classic" >&2; exit 1; }
[ -r "$SSH_PUBKEY_FILE" ] || { echo "cannot read $SSH_PUBKEY_FILE" >&2; exit 1; }

rm -rf "$out"
mkdir -p "$out"

# Fill in the templates. Python does the JSON quoting so names and keys with
# special characters can't break the assertion.
python3 - "$here" "$out" <<'EOF'
import datetime, json, os, sys
here, out = sys.argv[1], sys.argv[2]
now = datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0)
ts = now.isoformat()
until = (now + datetime.timedelta(days=int(os.environ["VALID_DAYS"]))).isoformat()

model = json.load(open(os.path.join(here, "model.json")))
brand = os.environ["BRAND_ID"] or model["brand-id"]
model.update({"authority-id": brand, "brand-id": brand, "timestamp": ts})

user = json.load(open(os.path.join(here, "system-user.json")))
user.update({
    "authority-id": brand,
    "brand-id": brand,
    "models": [model["model"]],
    "name": os.environ["USER_NAME"],
    "username": os.environ["USER_USERNAME"],
    "email": os.environ["USER_EMAIL"],
    "ssh-keys": [open(os.environ["SSH_PUBKEY_FILE"]).read().strip()],
    "since": ts,
    "until": until,
    "timestamp": ts,
})

json.dump(model, open(os.path.join(out, "model.json"), "w"), indent=2)
json.dump(user, open(os.path.join(out, "system-user.json"), "w"), indent=2)
print(f"model {model['model']} (brand {brand}); user {user['username']} until {until}")
EOF

# Signing prompts for the key's passphrase (once per assertion).
snap sign -k "$SNAP_KEY" "$out/model.json" > "$out/image.model"
snap sign -k "$SNAP_KEY" --chain "$out/system-user.json" > "$out/system-user.assert"

ubuntu-image snap --assertion "$out/system-user.assert" -O "$out" "$out/image.model"
xz -T0 -1 -k -f "$out/pi.img"
ls -lh "$out"/pi.img*

#!/bin/sh
# Build a minimal Alpine microvm image for the wash-vm/vm harness (docs/NET.md
# §8.4). For B0 the image is an Alpine-minirootfs initramfs + a static guest
# agent, booted with Alpine's vmlinuz-virt; no apk/virt tools required on the
# host. Later phases swap to a baked qcow2 + 9p dev-share.
#
# Outputs: out/vm/vmlinuz, out/vm/initramfs.gz  (git-ignored build artifacts)
set -eu

ALPINE_VER="${ALPINE_VER:-3.23.0}"
ARCH=x86_64
MIRROR="https://dl-cdn.alpinelinux.org/alpine/latest-stable/releases/${ARCH}"

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT_DIR/out/vm"
BUILD="$OUT/build"
mkdir -p "$BUILD"

# Base rootfs = Alpine + NetworkManager (the type-2 backend, docs/NET.md §5) +
# linux-virt (so the kernel AND its modules — virtio_net for the NICs, 8021q,
# bridge — come from the SAME package and always match). The host has no apk, so
# we render the rootfs in a throwaway Alpine container and export it. Cached;
# rebuilt only when this package set changes.
NM_PKGS="networkmanager networkmanager-cli networkmanager-wifi dbus wpa_supplicant eudev linux-virt"
ROOTFS_TAR="$BUILD/alpine-nm.tar"
PKG_MARK="$BUILD/.alpine-nm.pkgs"
if [ ! -f "$ROOTFS_TAR" ] || [ "$(cat "$PKG_MARK" 2>/dev/null)" != "$ALPINE_VER:$NM_PKGS" ]; then
  echo ">> rendering Alpine+NM rootfs via Docker (host has no apk)"
  command -v docker >/dev/null || { echo "!! docker required to build the NM rootfs" >&2; exit 1; }
  docker rm -f washnm-build >/dev/null 2>&1 || true
  docker run --name washnm-build "alpine:${ALPINE_VER%.*}" \
    sh -c "apk add --no-cache $NM_PKGS" >/dev/null
  docker export washnm-build -o "$ROOTFS_TAR"
  docker rm washnm-build >/dev/null
  printf '%s' "$ALPINE_VER:$NM_PKGS" > "$PKG_MARK"
fi

echo ">> building static guest agent + nm probe"
GOARCH="$( [ "$ARCH" = x86_64 ] && echo amd64 || echo "$ARCH" )"
CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" \
  go build -trimpath -ldflags="-s -w" -o "$BUILD/washvm-agent" "$ROOT_DIR/cmd/washvm-agent"
# washnet-nmprobe: B4b smoke check that the pure-Go nm backend reaches NM in the
# guest (godbus → NetworkManager). Run over the ctl plane.
CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" \
  go build -trimpath -ldflags="-s -w" -o "$BUILD/washnet-nmprobe" "$ROOT_DIR/cmd/washnet-nmprobe"
# washnet-apply: render a UCI config to NM keyfiles and apply it to live NM.
CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" \
  go build -trimpath -ldflags="-s -w" -o "$BUILD/washnet-apply" "$ROOT_DIR/cmd/washnet-apply"

# The multicall wash binary (router + apps, incl. com.wash.net/.netd) is the
# real payload (docs/NET.md §8.3 — the VM serves everything). It embeds every
# FE bundle, so it can only be produced by the full web+go build: `make
# multicall`. Image building is file ops (§8.4), so we require it pre-built.
WASH_BIN="$ROOT_DIR/out/wash"
if [ ! -x "$WASH_BIN" ]; then
  echo "!! $WASH_BIN missing — run 'make multicall' first (builds the FE bundles + static multicall)" >&2
  exit 1
fi

echo ">> assembling rootfs"
RFS="$BUILD/root"
rm -rf "$RFS"
mkdir -p "$RFS"
tar -C "$RFS" -xf "$ROOTFS_TAR"
# Kernel comes from the same linux-virt package as the in-rootfs modules.
cp "$RFS/boot/vmlinuz-virt" "$OUT/vmlinuz"
install -Dm755 "$BUILD/washvm-agent" "$RFS/sbin/washvm-agent"

# Make NM actually manage the eth NICs: keyfile-only plugin (don't defer to
# Alpine's /etc/network/interfaces via ifupdown) + explicitly manage everything.
# Without this NM reports the devices "unmanaged" and won't apply our profiles.
mkdir -p "$RFS/etc/NetworkManager/conf.d"
cat > "$RFS/etc/NetworkManager/conf.d/10-wash.conf" <<'NMCONF'
[main]
plugins=keyfile
# Don't auto-create a "Wired connection N" per NIC: those would hold eth1/eth2/
# eth3 and conflict when wash applies a bridge-port / vlan onto them. wash owns
# the config; an unconfigured NIC just sits managed+disconnected until applied.
no-auto-default=*

[keyfile]
unmanaged-devices=none

[device]
match-device=interface-name:eth*
managed=1
NMCONF
install -Dm755 "$BUILD/washnet-nmprobe" "$RFS/usr/bin/washnet-nmprobe"
install -Dm755 "$BUILD/washnet-apply" "$RFS/usr/bin/washnet-apply"

# Bake the multicall + materialize the per-app symlinks the router scans. The
# host binary is the same arch (amd64), so run it on the host to emit symlinks
# into the guest apps dir; symlinks are just names (→ wash).
install -Dm755 "$WASH_BIN" "$RFS/usr/lib/wash/wash"
"$WASH_BIN" install-symlinks "$RFS/usr/lib/wash" >/dev/null
ln -sf ../lib/wash/wash "$RFS/usr/bin/wash"

cat > "$RFS/init" <<'INIT'
#!/bin/sh
# wash-vm guest init (docs/NET.md §8.3): bring up the basics, launch the real
# in-guest wash-router on the DATA plane (virtio-serial /dev/vport0p1 — a raw
# chardev), and hand off to the control agent on the raw serial control plane
# (ttyS1). The kernel logs to ttyS0 (the log plane).
mount -t proc proc /proc 2>/dev/null
mount -t sysfs sys /sys 2>/dev/null
mount -t devtmpfs dev /dev 2>/dev/null
mount -t tmpfs tmpfs /run 2>/dev/null

# Load the NIC driver + link drivers (the initramfs is module-bearing now, from
# linux-virt). virtio_net makes the qemu NICs appear as eth0..ethN; 8021q/bridge
# back the vlan/bridge devices NM creates (NM also autoloads these, but be
# explicit). Quiet if already built in.
for m in virtio_net 8021q bridge; do modprobe "$m" 2>/dev/null; done

# udev: NM refuses to manage a link until udev has initialized it (reason 71 —
# "link not initialized by udev"). Start eudev's udevd and populate the device
# db before NM comes up.
/sbin/udevd --daemon 2>/dev/null
udevadm trigger --action=add 2>/dev/null
udevadm settle --timeout=10 2>/dev/null

# Bring up D-Bus + NetworkManager so the netd NM backend (docs/NET.md §5 — the
# type-2 backend, driven pure-Go over D-Bus via godbus) has something to talk
# to. This is a hand-rolled init (no OpenRC), so start the daemons directly.
ip link set lo up 2>/dev/null
mkdir -p /run/dbus /run/NetworkManager /var/lib/NetworkManager /etc/NetworkManager/system-connections
[ -s /etc/machine-id ] || dbus-uuidgen --ensure=/etc/machine-id 2>/dev/null || dbus-uuidgen > /etc/machine-id
dbus-daemon --system 2>>/run/nm.log
( i=0; while [ ! -e /run/dbus/system_bus_socket ] && [ $i -lt 50 ]; do i=$((i+1)); sleep 0.1; done )
NetworkManager --no-daemon >>/run/nm.log 2>&1 &

# Serve the wash UI + wire over the data port. The virtio-serial port device
# (/dev/vport0p1) only appears once the HOST connects the data chardev (the
# harness dials it in Launch), so wait generously — the router is pointless
# without it. Re-launch on exit (the host may reconnect across browser
# sessions). The router scans /usr/lib/wash for apps (com.wash.net, .netd, the
# session desktop, …); logs to /run/wash-router.log for ctl-plane inspection.
# The port is a raw chardev, so no stty/raw is needed.
mkdir -p /run
( i=0; while [ ! -e /dev/vport0p1 ] && [ $i -lt 600 ]; do i=$((i+1)); sleep 0.1; done
  while :; do
    /usr/lib/wash/wash-router \
        --transport=virtio-console:/dev/vport0p1 \
        --apps-dir=/usr/lib/wash \
        >>/run/wash-router.log 2>&1
    echo "wash-router exited rc=$? — respawn in 1s" >>/run/wash-router.log
    sleep 1
  done ) &

exec /sbin/washvm-agent /dev/ttyS1
INIT
chmod +x "$RFS/init"

echo ">> packing initramfs (root-owned: reserved ids like com.wash.netd need a"
echo "   root-owned/non-world-writable binary to be served — see registry.go)"
( cd "$RFS" && find . | cpio -o -H newc -R 0:0 2>/dev/null | gzip -9 ) > "$OUT/initramfs.gz"

echo ">> done:"
ls -lh "$OUT/vmlinuz" "$OUT/initramfs.gz"

#!/bin/sh
# Build a minimal Debian / ifupdown microvm for the wash-vm/vm harness
# (docs/NET-BACKENDS.md §6). Debian is the ifupdown test bed: classic
# /etc/network/interfaces is the manager (no netplan, no NetworkManager), wash's
# ifupdown backend reads + owns the file and drives ifup/ifdown, and autodetect
# resolves to ifupdown.
#
# Boots all-in-initramfs with systemd as PID1, like the Fedora/Ubuntu images.
#
# Outputs (git-ignored): out/vm/vmlinuz-debian, out/vm/initramfs-debian.gz
set -eu

DEBIAN_VER="${DEBIAN_VER:-12}"
ARCH=x86_64
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT_DIR/out/vm"
BUILD="$OUT/build-debian"
mkdir -p "$BUILD"

# shellcheck source=scripts/lib/wash-vm-payload.sh
. "$ROOT_DIR/scripts/lib/wash-vm-payload.sh"

# Base rootfs = Debian + ifupdown + the helpers it shells to (bridge-utils for
# bridges, vlan for vlans, isc-dhcp-client for `inet dhcp`) + a kernel. NO
# netplan, NO NetworkManager — ifupdown is the sole manager, so autodetect picks
# it. Rendered in a throwaway Debian container; cached.
DEB_PKGS="systemd systemd-sysv ifupdown isc-dhcp-client iproute2 iputils-ping bridge-utils vlan udev linux-image-amd64 bash"
ROOTFS_TAR="$BUILD/debian.tar"
PKG_MARK="$BUILD/.debian.pkgs"
RENDER_VER="1"
if [ ! -f "$ROOTFS_TAR" ] || [ "$(cat "$PKG_MARK" 2>/dev/null)" != "$DEBIAN_VER:$RENDER_VER:$DEB_PKGS" ]; then
	echo ">> rendering Debian+ifupdown rootfs via Docker (host needs no apt)"
	command -v docker >/dev/null || { echo "!! docker required to build the rootfs" >&2; exit 1; }
	docker rm -f washdeb-build >/dev/null 2>&1 || true
	docker run --name washdeb-build "debian:${DEBIAN_VER}" bash -c "
		export DEBIAN_FRONTEND=noninteractive &&
		apt-get update -qq &&
		apt-get install -y --no-install-recommends $DEB_PKGS &&
		useradd -m -s /bin/bash wash && passwd -d wash &&
		apt-get clean && rm -rf /var/lib/apt/lists/*" >/dev/null
	docker export washdeb-build -o "$ROOTFS_TAR"
	docker rm washdeb-build >/dev/null
	printf '%s' "$DEBIAN_VER:$RENDER_VER:$DEB_PKGS" > "$PKG_MARK"
fi

wvm_build_core "$BUILD" "$ROOT_DIR" "$ARCH"
wvm_build_cli "$BUILD" "$ROOT_DIR" "$ARCH" washnet-apply washnet-read
WASH_BIN="$(wvm_require_multicall "$ROOT_DIR")"

echo ">> assembling rootfs"
RFS="$BUILD/root"
[ -d "$RFS" ] && chmod -R u+w "$RFS" 2>/dev/null
rm -rf "$RFS"
mkdir -p "$RFS"
tar -C "$RFS" -xf "$ROOTFS_TAR"
chmod -R u+w "$RFS"

KVER="$(ls "$RFS/lib/modules" | head -1)"
[ -n "$KVER" ] || { echo "!! no kernel modules in rootfs (linux-image-amd64 missing?)" >&2; exit 1; }
if [ -f "$RFS/boot/vmlinuz-$KVER" ]; then
	cp "$RFS/boot/vmlinuz-$KVER" "$OUT/vmlinuz-debian"
else
	cp "$(ls "$RFS"/boot/vmlinuz-* | head -1)" "$OUT/vmlinuz-debian"
fi

wvm_stage_payload "$RFS" "$BUILD" "$WASH_BIN"
install -Dm755 "$BUILD/washnet-apply" "$RFS/usr/bin/washnet-apply"
install -Dm755 "$BUILD/washnet-read" "$RFS/usr/bin/washnet-read"

# --- Debian integration: ifupdown is the manager -----------------------------
# Enable Debian's networking.service (runs ifup -a at boot). No netplan / NM.
mkdir -p "$RFS/etc/systemd/system/multi-user.target.wants"
ln -sf /lib/systemd/system/networking.service \
	"$RFS/etc/systemd/system/multi-user.target.wants/networking.service" 2>/dev/null || true

# Stable eth0..eth3 names.
mkdir -p "$RFS/etc/udev/rules.d"
ln -sf /dev/null "$RFS/etc/udev/rules.d/80-net-setup-link.rules"

# Bake the box's /etc/network/interfaces: lo + eth0 DHCP (baseline route the
# commit-confirm lock-out protects, §7) + a static br-lan bridge over eth1/eth2.
# wash TAKES OVER this file on apply.
mkdir -p "$RFS/etc/network"
cat > "$RFS/etc/network/interfaces" <<'IFACES'
auto lo
iface lo inet loopback

auto eth0
iface eth0 inet dhcp

auto br-lan
iface br-lan inet static
    address 10.0.50.1/24
    bridge_ports eth1 eth2
IFACES

# --- boot: systemd as PID1 from the initramfs --------------------------------
ln -sf /lib/systemd/systemd "$RFS/init"
ln -sf /lib/systemd/systemd "$RFS/sbin/init" 2>/dev/null || true
rm -f "$RFS/etc/initrd-release"
ln -sf /lib/systemd/system/multi-user.target "$RFS/etc/systemd/system/default.target"
: > "$RFS/etc/machine-id"

cat > "$RFS/etc/systemd/system/wash-agent.service" <<'SVC'
[Unit]
Description=wash-vm control-plane agent (ttyS1)
DefaultDependencies=no
After=systemd-tmpfiles-setup.service
Before=multi-user.target

[Service]
ExecStart=/sbin/washvm-agent /dev/ttyS1
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
SVC
ln -sf /etc/systemd/system/wash-agent.service \
	"$RFS/etc/systemd/system/multi-user.target.wants/wash-agent.service"

# autodetect → ifupdown (ifup present + /etc/network/interfaces configured; no
# netplan / NM).
cat > "$RFS/usr/sbin/wash-router-launch" <<'LAUNCH'
#!/bin/sh
export SHELL=/bin/bash HOME=/home/wash TERM=xterm-256color
export PATH="/usr/bin:/bin:/usr/sbin:/sbin:/usr/lib/wash"
mkdir -p /tmp && chmod 1777 /tmp
mkdir -p /home/wash/.config/wash && chown -R wash /home/wash 2>/dev/null
i=0; while [ ! -e /dev/vport0p1 ] && [ "$i" -lt 600 ]; do i=$((i+1)); sleep 0.1; done
chown wash /dev/vport0p1 2>/dev/null
while :; do
  su -m wash -c '/usr/lib/wash/wash-router --transport=virtio-console:/dev/vport0p1 --apps-dir=/usr/lib/wash' >> /run/wash-router.log 2>&1
  echo "wash-router exited rc=$? — respawn in 1s" >> /run/wash-router.log
  sleep 1
done
LAUNCH
chmod +x "$RFS/usr/sbin/wash-router-launch"

cat > "$RFS/etc/systemd/system/wash-router.service" <<'SVC'
[Unit]
Description=wash desktop (router + apps)
After=networking.service dbus.service
Wants=networking.service

[Service]
ExecStart=/usr/sbin/wash-router-launch
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
SVC
ln -sf /etc/systemd/system/wash-router.service \
	"$RFS/etc/systemd/system/multi-user.target.wants/wash-router.service"

depmod -b "$RFS" "$KVER" 2>/dev/null || true

wvm_pack_initramfs "$RFS" "$OUT/initramfs-debian.gz"

echo ">> done:"
ls -lh "$OUT/vmlinuz-debian" "$OUT/initramfs-debian.gz"

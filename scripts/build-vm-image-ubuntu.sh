#!/bin/sh
# Build a minimal Ubuntu / netplan microvm for the wash-vm/vm harness
# (docs/NET-BACKENDS.md §6). Ubuntu is the netplan test bed: netplan.io is the
# authority config layer (rendering to systemd-networkd underneath), wash's
# netplan backend reads /etc/netplan via `netplan get` and owns it on apply, and
# autodetect (WASH_NETD_BACKEND=auto) resolves to netplan.
#
# Boots all-in-initramfs with systemd as PID1, exactly like the Fedora image.
#
# Outputs (git-ignored): out/vm/vmlinuz-ubuntu, out/vm/initramfs-ubuntu.gz
set -eu

UBUNTU_VER="${UBUNTU_VER:-24.04}"
ARCH=x86_64
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT_DIR/out/vm"
BUILD="$OUT/build-ubuntu"
mkdir -p "$BUILD"

# shellcheck source=scripts/lib/wash-vm-payload.sh
. "$ROOT_DIR/scripts/lib/wash-vm-payload.sh"

# Base rootfs = Ubuntu + netplan.io + systemd-networkd (the under-renderer) + a
# kernel (linux-image-virtual) + the workers wash drives. NO NetworkManager —
# netplan is the manager, so autodetect picks it. Rendered in a throwaway Ubuntu
# container; cached, rebuilt only when the package set changes.
UB_PKGS="systemd systemd-sysv systemd-resolved netplan.io iproute2 iputils-ping wireguard-tools dbus bash util-linux linux-image-virtual"
ROOTFS_TAR="$BUILD/ubuntu.tar"
PKG_MARK="$BUILD/.ubuntu.pkgs"
RENDER_VER="1"
if [ ! -f "$ROOTFS_TAR" ] || [ "$(cat "$PKG_MARK" 2>/dev/null)" != "$UBUNTU_VER:$RENDER_VER:$UB_PKGS" ]; then
	echo ">> rendering Ubuntu+netplan rootfs via Docker (host needs no apt)"
	command -v docker >/dev/null || { echo "!! docker required to build the rootfs" >&2; exit 1; }
	docker rm -f washubuntu-build >/dev/null 2>&1 || true
	docker run --name washubuntu-build "ubuntu:${UBUNTU_VER}" bash -c "
		export DEBIAN_FRONTEND=noninteractive &&
		apt-get update -qq &&
		apt-get install -y --no-install-recommends $UB_PKGS &&
		useradd -m -s /bin/bash wash && passwd -d wash &&
		apt-get clean && rm -rf /var/lib/apt/lists/*" >/dev/null
	docker export washubuntu-build -o "$ROOTFS_TAR"
	docker rm washubuntu-build >/dev/null
	printf '%s' "$UBUNTU_VER:$RENDER_VER:$UB_PKGS" > "$PKG_MARK"
fi

# Core payload (agent + setuid-root netd trampoline) + the in-guest CLIs the e2e
# drives (washnet-read is backend-aware; washnet-apply renders+applies).
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

# Extract the kernel for -kernel boot; modules stay in the rootfs so udev
# autoloads virtio_net/8021q/bridge. Ubuntu's linux-image-virtual ships vmlinuz
# under /boot and modules under /lib/modules/<ver>/.
KVER="$(ls "$RFS/lib/modules" | head -1)"
[ -n "$KVER" ] || { echo "!! no kernel modules in rootfs (linux-image-virtual missing?)" >&2; exit 1; }
if [ -f "$RFS/boot/vmlinuz-$KVER" ]; then
	cp "$RFS/boot/vmlinuz-$KVER" "$OUT/vmlinuz-ubuntu"
else
	cp "$(ls "$RFS"/boot/vmlinuz-* | head -1)" "$OUT/vmlinuz-ubuntu"
fi

wvm_stage_payload "$RFS" "$BUILD" "$WASH_BIN"
install -Dm755 "$BUILD/washnet-apply" "$RFS/usr/bin/washnet-apply"
install -Dm755 "$BUILD/washnet-read" "$RFS/usr/bin/washnet-read"

# --- Ubuntu integration: netplan is the authority layer ----------------------
# Enable systemd-networkd (the under-renderer netplan generates to) + resolved.
# No NetworkManager installed — netplan owns the box, so autodetect picks it.
mkdir -p "$RFS/etc/systemd/system/multi-user.target.wants" "$RFS/etc/systemd/system/sockets.target.wants"
ln -sf /usr/lib/systemd/system/systemd-networkd.service \
	"$RFS/etc/systemd/system/multi-user.target.wants/systemd-networkd.service" 2>/dev/null || true
ln -sf /usr/lib/systemd/system/systemd-networkd.socket \
	"$RFS/etc/systemd/system/sockets.target.wants/systemd-networkd.socket" 2>/dev/null || true
ln -sf /usr/lib/systemd/system/systemd-resolved.service \
	"$RFS/etc/systemd/system/multi-user.target.wants/systemd-resolved.service" 2>/dev/null || true

# Keep stable eth0..eth3 names (mask the predictable-naming udev rule).
mkdir -p "$RFS/etc/udev/rules.d"
ln -sf /dev/null "$RFS/etc/udev/rules.d/80-net-setup-link.rules"

# Bake the box's netplan config: eth0 DHCP (baseline route the commit-confirm
# lock-out check protects, §7) + a static br-lan bridge over eth1/eth2 (a
# non-trivial shape — bridge + members + static + dns — so the read validation
# proves the netplan backend handles real configs, not just eth/dhcp). wash
# TAKES OVER this dir on apply.
mkdir -p "$RFS/etc/netplan"
cat > "$RFS/etc/netplan/00-wash-default.yaml" <<'NETPLAN'
network:
  version: 2
  renderer: networkd
  ethernets:
    eth0:
      dhcp4: true
    eth1: {}
    eth2: {}
  bridges:
    br-lan:
      interfaces: [eth1, eth2]
      addresses: [10.0.50.1/24]
      nameservers:
        addresses: [10.0.50.1]
NETPLAN
chmod 0600 "$RFS/etc/netplan/00-wash-default.yaml"

# --- boot: systemd as PID1 from the initramfs --------------------------------
ln -sf /usr/lib/systemd/systemd "$RFS/init"
ln -sf /usr/lib/systemd/systemd "$RFS/sbin/init" 2>/dev/null || true
rm -f "$RFS/etc/initrd-release"
ln -sf /usr/lib/systemd/system/multi-user.target "$RFS/etc/systemd/system/default.target"
: > "$RFS/etc/machine-id"

# wash-agent: ctl-plane agent on ttyS1 (harness WaitReady waits for its hello).
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

# wash-router-launch: no WASH_NETD_BACKEND → autodetect → netplan (binary present
# + /etc/netplan config). Hands the data-plane fd to the unprivileged wash user.
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
After=systemd-networkd.service dbus.service
Wants=systemd-networkd.service

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

wvm_pack_initramfs "$RFS" "$OUT/initramfs-ubuntu.gz"

echo ">> done:"
ls -lh "$OUT/vmlinuz-ubuntu" "$OUT/initramfs-ubuntu.gz"

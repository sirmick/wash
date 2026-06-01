#!/bin/sh
# Build a minimal Fedora / systemd-networkd microvm for the wash-vm/vm harness
# (docs/NET.md §8.4, Phase C). Fedora is the networkd ("own-it") test bed exactly
# as Alpine is the NM one: systemd-networkd is the active link manager (no
# NetworkManager is installed), wash renders .network/.netdev and drives
# networkctl, and autodetect (WASH_NETD_BACKEND=auto) resolves to networkd.
#
# Boots all-in-initramfs with systemd as PID1: the kernel runs /init → systemd,
# and because there is NO /etc/initrd-release systemd runs as a normal real-root
# init (not initrd mode). This reuses the harness's existing initramfs path — no
# squashfs/disk boot needed — at the cost of more RAM (the harness runs 1G).
#
# Outputs (git-ignored): out/vm/vmlinuz-fedora, out/vm/initramfs-fedora.gz
set -eu

FEDORA_VER="${FEDORA_VER:-41}"
ARCH=x86_64
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT_DIR/out/vm"
BUILD="$OUT/build-fedora"
mkdir -p "$BUILD"

# shellcheck source=scripts/lib/wash-vm-payload.sh
. "$ROOT_DIR/scripts/lib/wash-vm-payload.sh"

# Base rootfs = Fedora + systemd-networkd + the kernel (for vmlinuz + virtio
# modules) + the workers wash drives (iproute for `ip`, wireguard-tools for
# `wg`). NO NetworkManager — networkd is the sole manager (the own-it posture),
# so autodetect picks it. Rendered in a throwaway Fedora container and exported;
# cached, rebuilt only when the package set changes.
FED_PKGS="systemd systemd-networkd systemd-resolved kernel-core iproute iputils wireguard-tools dbus-daemon passwd bash util-linux"
ROOTFS_TAR="$BUILD/fedora.tar"
PKG_MARK="$BUILD/.fedora.pkgs"
RENDER_VER="1"
if [ ! -f "$ROOTFS_TAR" ] || [ "$(cat "$PKG_MARK" 2>/dev/null)" != "$FEDORA_VER:$RENDER_VER:$FED_PKGS" ]; then
	echo ">> rendering Fedora+networkd rootfs via Docker (host needs no dnf)"
	command -v docker >/dev/null || { echo "!! docker required to build the rootfs" >&2; exit 1; }
	docker rm -f washfed-build >/dev/null 2>&1 || true
	docker run --name washfed-build "fedora:${FEDORA_VER}" bash -c "
		dnf -y install --setopt=install_weak_deps=False $FED_PKGS &&
		useradd -m -s /bin/bash wash &&
		passwd -d wash &&
		dnf clean all" >/dev/null
	docker export washfed-build -o "$ROOTFS_TAR"
	docker rm washfed-build >/dev/null
	printf '%s' "$FEDORA_VER:$RENDER_VER:$FED_PKGS" > "$PKG_MARK"
fi

# Core payload (agent + setuid-root netd trampoline) + washnet-apply (the in-guest
# apply tool the e2e drives; backend-aware, picks networkd via WASH_NETD_BACKEND).
wvm_build_core "$BUILD" "$ROOT_DIR" "$ARCH"
wvm_build_cli "$BUILD" "$ROOT_DIR" "$ARCH" washnet-apply
WASH_BIN="$(wvm_require_multicall "$ROOT_DIR")"

echo ">> assembling rootfs"
RFS="$BUILD/root"
# A prior run leaves read-only dirs (Fedora /usr/sbin 0555, /root 0550) we must
# loosen before rm can unlink them.
[ -d "$RFS" ] && chmod -R u+w "$RFS" 2>/dev/null
rm -rf "$RFS"
mkdir -p "$RFS"
tar -C "$RFS" -xf "$ROOTFS_TAR"
# Fedora ships system dirs (/usr/bin, /usr/sbin) as 0555 — make the tree writable
# so payload staging can install into them. Modes don't matter in a throwaway
# RAM image; the cpio -R 0:0 pack re-owns everything to root regardless.
chmod -R u+w "$RFS"

# Extract the kernel for -kernel boot; the modules stay in the rootfs so udev
# autoloads virtio_net/8021q/bridge. Fedora's kernel-core ships vmlinuz under
# /lib/modules/<ver>/.
KVER="$(ls "$RFS/lib/modules" | head -1)"
[ -n "$KVER" ] || { echo "!! no kernel in rootfs (kernel-core missing?)" >&2; exit 1; }
cp "$RFS/lib/modules/$KVER/vmlinuz" "$OUT/vmlinuz-fedora"

wvm_stage_payload "$RFS" "$BUILD" "$WASH_BIN"
install -Dm755 "$BUILD/washnet-apply" "$RFS/usr/bin/washnet-apply"

# --- Fedora integration: networkd is the manager -----------------------------
# The system D-Bus bus: dbus.socket is enabled, but it socket-activates
# `dbus.service`, an alias that only exists once a bus impl is enabled. Point it
# at dbus-broker (Fedora's default) so networkctl / sd-bus can reach the bus.
ln -sf /usr/lib/systemd/system/dbus-broker.service "$RFS/etc/systemd/system/dbus.service"

# Enable systemd-networkd (+ resolved) as the active link manager — the own-it
# posture autodetect keys on. No NetworkManager is installed, nothing to mask.
mkdir -p "$RFS/etc/systemd/system/multi-user.target.wants"
ln -sf /usr/lib/systemd/system/systemd-networkd.service \
	"$RFS/etc/systemd/system/multi-user.target.wants/systemd-networkd.service"
ln -sf /usr/lib/systemd/system/systemd-networkd.socket \
	"$RFS/etc/systemd/system/sockets.target.wants/systemd-networkd.socket" 2>/dev/null || true
ln -sf /usr/lib/systemd/system/systemd-resolved.service \
	"$RFS/etc/systemd/system/multi-user.target.wants/systemd-resolved.service" 2>/dev/null || true

# Keep stable eth0..eth3 names instead of systemd's predictable enp0sN. Mask the
# net-naming udev rule outright (a .link NamePolicy didn't override Fedora's
# 99-default.link, and net.ifnames=0 alone didn't suppress it) — the NICs then
# keep their kernel names, matching the Alpine image, the harness NICs, and the
# wash model's device conventions.
mkdir -p "$RFS/etc/udev/rules.d"
ln -sf /dev/null "$RFS/etc/udev/rules.d/80-net-setup-link.rules"

mkdir -p "$RFS/etc/systemd/network"

# Baseline connectivity so the box has a default route at boot (the commit-confirm
# lock-out check needs a route to protect, §7). wash OWNS this dir and rewrites
# it on apply, so this 90-* default is the pre-apply state only.
cat > "$RFS/etc/systemd/network/90-wash-default-eth0.network" <<'NET'
[Match]
Name=eth0

[Network]
DHCP=yes
NET

# netd (root via the setuid trampoline) writes units here.
chmod 0755 "$RFS/etc/systemd/network"

# --- boot: systemd as PID1 from the initramfs --------------------------------
# The kernel execs /init; point it at systemd. With NO /etc/initrd-release
# present (this is a full rootfs, not a dracut initramfs) systemd boots as a
# normal real-root init. Multi-user.target (no graphical) is the default.
ln -sf /usr/lib/systemd/systemd "$RFS/init"
ln -sf /usr/lib/systemd/systemd "$RFS/sbin/init" 2>/dev/null || true
rm -f "$RFS/etc/initrd-release"
ln -sf /usr/lib/systemd/system/multi-user.target "$RFS/etc/systemd/system/default.target"

# systemd needs a machine-id; an empty file makes it generate one (first-boot).
: > "$RFS/etc/machine-id"

# wash-agent: the ctl-plane agent on ttyS1 (the harness WaitReady waits for its
# hello). Up early — ordered before the desktop, after local-fs.
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

# wash-router-launch: hand the data-plane fd to the unprivileged 'wash' user,
# then run the whole desktop AS wash. WASH_NETD_BACKEND=auto → netd probes →
# networkd (NM absent). Re-runs forever (the router exits between browser
# sessions), exactly like the Alpine launcher.
cat > "$RFS/usr/sbin/wash-router-launch" <<'LAUNCH'
#!/bin/sh
export WASH_NETD_BACKEND=auto
export SHELL=/bin/bash HOME=/home/wash TERM=xterm-256color
export PATH="/usr/bin:/bin:/usr/sbin:/sbin:/usr/lib/wash"
mkdir -p /tmp && chmod 1777 /tmp
mkdir -p /home/wash/.config/wash && chown -R wash /home/wash 2>/dev/null
i=0; while [ ! -e /dev/vport0p1 ] && [ "$i" -lt 600 ]; do i=$((i+1)); sleep 0.1; done
chown wash /dev/vport0p1 2>/dev/null
while :; do
  su -l wash -c '/usr/lib/wash/wash-router --transport=virtio-console:/dev/vport0p1 --apps-dir=/usr/lib/wash' >> /run/wash-router.log 2>&1
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

# depmod so udev can resolve virtio module deps for autoload.
depmod -b "$RFS" "$KVER" 2>/dev/null || true

wvm_pack_initramfs "$RFS" "$OUT/initramfs-fedora.gz"

echo ">> done:"
ls -lh "$OUT/vmlinuz-fedora" "$OUT/initramfs-fedora.gz"

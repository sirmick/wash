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
# Service set: NetworkManager + OpenRC (the image now boots via OpenRC, not a
# hand-rolled /init). We ship custom minimal /etc/init.d scripts (below) rather
# than the distro *-openrc subpackages: those pull in alpine-base's disk-oriented
# sysinit services (need sysfs / localmount / fsck) that don't apply to a
# run-from-RAM initramfs. openrc itself gives us runlevels + supervise-daemon.
NM_PKGS="networkmanager networkmanager-cli networkmanager-wifi dbus polkit wpa_supplicant eudev linux-virt bash openrc"
# The guest runs the wash desktop as the unprivileged 'wash' user (in the netdev
# group so NM/polkit lets it manage networking — see 49-wash-nm.rules).
WASH_USER_SETUP="addgroup -S netdev 2>/dev/null; adduser -D -h /home/wash -s /bin/bash -G netdev wash; passwd -u wash 2>/dev/null || true"
ROOTFS_TAR="$BUILD/alpine-nm.tar"
PKG_MARK="$BUILD/.alpine-nm.pkgs"
RENDER_VER="2-openrc-washuser" # bump to force a re-render when this setup changes
if [ ! -f "$ROOTFS_TAR" ] || [ "$(cat "$PKG_MARK" 2>/dev/null)" != "$ALPINE_VER:$RENDER_VER:$NM_PKGS" ]; then
  echo ">> rendering Alpine+NM+OpenRC rootfs via Docker (host has no apk)"
  command -v docker >/dev/null || { echo "!! docker required to build the NM rootfs" >&2; exit 1; }
  docker rm -f washnm-build >/dev/null 2>&1 || true
  docker run --name washnm-build "alpine:${ALPINE_VER%.*}" \
    sh -c "apk add --no-cache $NM_PKGS && $WASH_USER_SETUP" >/dev/null
  docker export washnm-build -o "$ROOTFS_TAR"
  docker rm washnm-build >/dev/null
  printf '%s' "$ALPINE_VER:$RENDER_VER:$NM_PKGS" > "$PKG_MARK"
fi

# shellcheck source=scripts/lib/wash-vm-payload.sh
. "$ROOT_DIR/scripts/lib/wash-vm-payload.sh"

# Core payload (agent + setuid-root netd trampoline) + the NM-backend CLIs this
# image ships: washnet-nmprobe (B4b godbus→NM smoke check), -apply (UCI→NM
# keyfiles→live NM), -read, -cc. The multicall wash is the real payload and must
# be pre-built (make multicall).
wvm_build_core "$BUILD" "$ROOT_DIR" "$ARCH"
wvm_build_cli "$BUILD" "$ROOT_DIR" "$ARCH" washnet-nmprobe washnet-apply washnet-read washnet-cc
WASH_BIN="$(wvm_require_multicall "$ROOT_DIR")"

echo ">> assembling rootfs"
RFS="$BUILD/root"
rm -rf "$RFS"
mkdir -p "$RFS"
tar -C "$RFS" -xf "$ROOTFS_TAR"
# Kernel comes from the same linux-virt package as the in-rootfs modules.
cp "$RFS/boot/vmlinuz-virt" "$OUT/vmlinuz"

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

# Polkit rule: the guest is headless — no logind session, no auth agent — so
# NM's authorization checks (network-control to activate a bridge/vlan,
# settings.modify.system to write a profile) have nothing to prompt and fail with
# "Authorization request cancelled". Authorize every NetworkManager action for
# root AND the netdev group (the 'wash' user is a member), so the unprivileged
# desktop can still drive commit-confirm. (NM 1.54's auth-polkit=false bypass is
# not honoured from conf.d here, so we run a real polkitd + this rule instead.)
mkdir -p "$RFS/etc/polkit-1/rules.d"
cat > "$RFS/etc/polkit-1/rules.d/49-wash-nm.rules" <<'PKRULE'
polkit.addRule(function (action, subject) {
  if (action.id.indexOf("org.freedesktop.NetworkManager.") === 0 &&
      (subject.user === "root" || subject.isInGroup("netdev"))) {
    return polkit.Result.YES;
  }
});
PKRULE

# D-Bus bus policy: separate from polkit, the system bus's own send-policy
# decides whether a message even REACHES NetworkManager. By default only root may
# send to org.freedesktop.NetworkManager; the unprivileged 'wash' netd (uid 1000)
# gets "Rejected send message" on ReloadConnections/Activate. Allow the netdev
# group to send to NM (polkit still gates the privileged actions — see above).
mkdir -p "$RFS/etc/dbus-1/system.d"
cat > "$RFS/etc/dbus-1/system.d/wash-nm.conf" <<'DBUSCONF'
<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <policy group="netdev">
    <allow send_destination="org.freedesktop.NetworkManager"/>
    <allow receive_sender="org.freedesktop.NetworkManager"/>
  </policy>
</busconfig>
DBUSCONF

install -Dm755 "$BUILD/washnet-nmprobe" "$RFS/usr/bin/washnet-nmprobe"
install -Dm755 "$BUILD/washnet-apply" "$RFS/usr/bin/washnet-apply"
install -Dm755 "$BUILD/washnet-read" "$RFS/usr/bin/washnet-read"
install -Dm755 "$BUILD/washnet-cc" "$RFS/usr/bin/washnet-cc"

# Bake the agent + multicall + per-app symlinks, and the setuid-root netd
# trampoline (cpio -R 0:0 below makes it root-owned so the setuid bit grants
# root). Shared with every distro image — see scripts/lib/wash-vm-payload.sh.
wvm_stage_payload "$RFS" "$BUILD" "$WASH_BIN"

# --- boot: busybox-init → OpenRC (docs/NET.md §8.4) -------------------------
# The kernel runs /init from the initramfs; we exec busybox init, which reads
# /etc/inittab and hands the runlevels to OpenRC. early.sh does the imperative
# run-from-RAM bring-up OpenRC's disk sysinit can't; the daemons are OpenRC
# services (supervise-daemon, so they restart on exit).
ln -sf /bin/busybox "$RFS/sbin/init"

cat > "$RFS/init" <<'INIT'
#!/bin/sh
exec /sbin/init
INIT
chmod +x "$RFS/init"

cat > "$RFS/etc/inittab" <<'INITTAB'
::sysinit:/etc/wash-early.sh
::sysinit:/sbin/openrc sysinit
::sysinit:/sbin/openrc boot
::wait:/sbin/openrc default
::ctrlaltdel:/sbin/reboot
::shutdown:/sbin/openrc shutdown
INITTAB

cat > "$RFS/etc/wash-early.sh" <<'EARLY'
#!/bin/sh
# Imperative bring-up for the run-from-RAM initramfs — the bits OpenRC's
# disk-oriented sysinit would normally handle: core mounts, PTYs, kernel
# modules, udev. (Daemons are OpenRC services; see /etc/init.d/wash-*.)
mount -t proc proc /proc 2>/dev/null
mount -t sysfs sys /sys 2>/dev/null
mount -t devtmpfs dev /dev 2>/dev/null
mount -t tmpfs tmpfs /run 2>/dev/null
# /tmp must be world-writable+sticky so the unprivileged 'wash' router can bind
# its control socket (/tmp/wash-<uid>.sock).
mkdir -p /tmp && chmod 1777 /tmp
mkdir -p /dev/pts /dev/shm /run/openrc /run/dbus /run/NetworkManager \
         /var/lib/NetworkManager /etc/NetworkManager/system-connections
# netd runs as 'wash' and writes NM keyfiles here, so the dir must be wash-owned
# (0700 keeps the connection files non-world-readable, as NM requires).
chown wash /etc/NetworkManager/system-connections 2>/dev/null
chmod 0700 /etc/NetworkManager/system-connections 2>/dev/null
# The initramfs is packed root-owned (cpio -R 0:0, for the reserved-id trust),
# which also resets the wash home; chown it back so the desktop can write its
# config (~/.config/wash etc.).
mkdir -p /home/wash && chown -R wash /home/wash 2>/dev/null
mount -t devpts -o gid=5,mode=620,ptmxmode=666 devpts /dev/pts 2>/dev/null
[ -e /dev/ptmx ] || ln -s pts/ptmx /dev/ptmx
mount -t tmpfs shm /dev/shm 2>/dev/null
for m in virtio_net 8021q bridge; do modprobe "$m" 2>/dev/null; done
ip link set lo up 2>/dev/null
[ -s /etc/machine-id ] || dbus-uuidgen --ensure=/etc/machine-id 2>/dev/null
# udev so NM will manage the NICs (otherwise reason 71, "not initialized by udev").
/sbin/udevd --daemon 2>/dev/null
udevadm trigger --action=add 2>/dev/null
udevadm settle --timeout=10 2>/dev/null
# The router owns the single virtio data-plane fd and must be re-run on every
# host (re)connect — semantics OpenRC's start-stop-daemon mishandled (it
# multi-started the loop, racing to open the exclusive fd → fork-storm → OOM).
# So supervise it with a plain backgrounded loop, exactly as the proven legacy
# init did; it's orphaned to init (pid 1) when this script exits. The normal
# daemons (dbus/polkit/NetworkManager/agent) are OpenRC services.
/sbin/wash-router-launch &
EARLY
chmod +x "$RFS/etc/wash-early.sh"

# --- OpenRC services --------------------------------------------------------
mkdir -p "$RFS/etc/init.d"

cat > "$RFS/etc/init.d/dbus" <<'SVC'
#!/sbin/openrc-run
description="D-Bus system message bus"
supervisor="supervise-daemon"
command="/usr/bin/dbus-daemon"
command_args="--system --nofork --nopidfile"
respawn_delay=1
start_pre() { mkdir -p /run/dbus; [ -s /etc/machine-id ] || dbus-uuidgen --ensure=/etc/machine-id; }
SVC

cat > "$RFS/etc/init.d/polkit" <<'SVC'
#!/sbin/openrc-run
description="polkit authorization daemon"
supervisor="supervise-daemon"
command="/usr/lib/polkit-1/polkitd"
command_args="--no-debug"
respawn_delay=1
depend() { need dbus; }
start_pre() { mkdir -p /run/polkit-1 /var/lib/polkit-1; }
SVC

cat > "$RFS/etc/init.d/networkmanager" <<'SVC'
#!/sbin/openrc-run
description="NetworkManager"
supervisor="supervise-daemon"
command="/usr/sbin/NetworkManager"
command_args="--no-daemon"
output_log="/run/nm.log"
error_log="/run/nm.log"
respawn_delay=1
depend() { need dbus; after polkit; }
start_pre() { mkdir -p /run/NetworkManager /var/lib/NetworkManager /etc/NetworkManager/system-connections; }
SVC

cat > "$RFS/etc/init.d/wash-agent" <<'SVC'
#!/sbin/openrc-run
description="wash-vm control-plane agent (ttyS1)"
supervisor="supervise-daemon"
command="/sbin/washvm-agent"
command_args="/dev/ttyS1"
respawn_delay=1
SVC

# Launcher: sets the desktop's environment (NM backend + a real shell for
# wash-term) and RE-RUNS the router forever. The router exits whenever the host
# drops the data plane (browser closes / between sessions); we must retry
# indefinitely until the next browser attaches — so the loop lives here rather
# than relying on supervise-daemon's bounded respawn-max (which gives up after a
# few pre-browser exits and leaves the router dead when the browser arrives).
cat > "$RFS/sbin/wash-router-launch" <<'LAUNCH'
#!/bin/sh
# Runs as root (from wash-early.sh) only long enough to hand the data-plane fd to
# the unprivileged 'wash' user, then runs the whole desktop (router + every app
# it spawns, incl. com.wash.netd) AS wash. netd drives NM over D-Bus authorized
# by the netdev group (wash is a member; see 49-wash-nm.rules) and writes its
# keyfiles into the wash-owned system-connections dir.
export WASH_NETD_BACKEND=nm
export SHELL=/bin/bash HOME=/home/wash TERM=xterm-256color
export PATH="/usr/bin:/bin:/usr/sbin:/sbin:/usr/lib/wash"
# Wait for the data chardev (created when the host attaches it in Launch).
i=0; while [ ! -e /dev/vport0p1 ] && [ "$i" -lt 600 ]; do i=$((i+1)); sleep 0.1; done
chown wash /dev/vport0p1 2>/dev/null   # the single virtio data-plane fd
while :; do
  su -m wash -c '/usr/lib/wash/wash-router --transport=virtio-console:/dev/vport0p1 --apps-dir=/usr/lib/wash' >> /run/wash-router.log 2>&1
  echo "wash-router exited rc=$? — respawn in 1s" >> /run/wash-router.log
  sleep 1
done
LAUNCH
chmod +x "$RFS/sbin/wash-router-launch"

chmod +x "$RFS"/etc/init.d/dbus "$RFS"/etc/init.d/polkit "$RFS"/etc/init.d/networkmanager \
         "$RFS"/etc/init.d/wash-agent

# Runlevels: agent in boot (up early, for washvm-run's WaitReady), the daemons
# in default. OpenRC orders them by the depend() chains above. (The router is a
# supervised loop launched from wash-early.sh, not an OpenRC service — see above.)
for rl in sysinit boot default shutdown; do mkdir -p "$RFS/etc/runlevels/$rl"; done
ln -sf /etc/init.d/wash-agent      "$RFS/etc/runlevels/boot/wash-agent"
ln -sf /etc/init.d/dbus            "$RFS/etc/runlevels/default/dbus"
ln -sf /etc/init.d/polkit          "$RFS/etc/runlevels/default/polkit"
ln -sf /etc/init.d/networkmanager  "$RFS/etc/runlevels/default/networkmanager"

wvm_pack_initramfs "$RFS" "$OUT/initramfs.gz"

echo ">> done:"
ls -lh "$OUT/vmlinuz" "$OUT/initramfs.gz"

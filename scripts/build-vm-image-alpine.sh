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

echo ">> fetching Alpine assets (cached in $BUILD)"
[ -f "$BUILD/minirootfs.tar.gz" ] || \
  curl -fsSL -o "$BUILD/minirootfs.tar.gz" "$MIRROR/alpine-minirootfs-${ALPINE_VER}-${ARCH}.tar.gz"
[ -f "$OUT/vmlinuz" ] || \
  curl -fsSL -o "$OUT/vmlinuz" "$MIRROR/netboot/vmlinuz-virt"

echo ">> building static guest agent"
CGO_ENABLED=0 GOOS=linux GOARCH="$( [ "$ARCH" = x86_64 ] && echo amd64 || echo "$ARCH" )" \
  go build -trimpath -ldflags="-s -w" -o "$BUILD/washvm-agent" "$ROOT_DIR/cmd/washvm-agent"

echo ">> assembling rootfs"
RFS="$BUILD/root"
rm -rf "$RFS"
mkdir -p "$RFS"
tar -C "$RFS" -xzf "$BUILD/minirootfs.tar.gz"
install -Dm755 "$BUILD/washvm-agent" "$RFS/sbin/washvm-agent"

cat > "$RFS/init" <<'INIT'
#!/bin/sh
# wash-vm guest init: bring up the basics and hand off to the control agent on
# the raw serial control plane (ttyS1). The kernel logs to ttyS0 (the log plane).
mount -t proc proc /proc 2>/dev/null
mount -t sysfs sys /sys 2>/dev/null
mount -t devtmpfs dev /dev 2>/dev/null
stty -F /dev/ttyS1 raw -echo -onlcr 2>/dev/null
exec /sbin/washvm-agent /dev/ttyS1
INIT
chmod +x "$RFS/init"

echo ">> packing initramfs"
( cd "$RFS" && find . | cpio -o -H newc 2>/dev/null | gzip -9 ) > "$OUT/initramfs.gz"

echo ">> done:"
ls -lh "$OUT/vmlinuz" "$OUT/initramfs.gz"

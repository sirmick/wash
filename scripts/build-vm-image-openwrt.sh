#!/usr/bin/env sh
# Build the OpenWRT x86-64 router VM via OpenWRT's Image Builder (run in Docker),
# with the static washnet CLIs baked into /usr/bin (Approach B / Phase D,
# docs/NET-ROUTER-UI.md). Unlike the agent-initramfs distros this isn't a custom
# rootfs render — Image Builder assembles OpenWRT's own prebuilt packages, so the
# whole router stack (uci/fw4/odhcpd/dnsmasq/netifd) is the real thing; we just
# add our CLIs via a FILES overlay and pick a couple of router packages.
#
# Output (git-ignored): out/vm/openwrt.img — a bootable router with washnet aboard.
# Used by wash-vm/vm/openwrt_test.go (the OpenWRT router truth test).
set -eu

VER="${OPENWRT_VERSION:-24.10.6}"
IB="openwrt/imagebuilder:x86-64-$VER"
# Extra packages beyond the generic profile's defaults (which already include
# dnsmasq/firewall4/odhcpd/netifd). dnsmasq-full gives full DNS (hostnames,
# split-horizon); ip-full the full iproute2 the test assertions use. 802.1q VLANs
# are built into the kernel/netifd on modern OpenWRT (DSA) — no kmod needed.
PKGS="${OPENWRT_PACKAGES:-dnsmasq-full -dnsmasq ip-full}"

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT_DIR/out/vm"
mkdir -p "$OUT"

echo ">> build static washnet CLIs (baked into the image)"
( cd "$ROOT_DIR" && CGO_ENABLED=0 go build -o "$OUT/owrt-files/usr/bin/washnet-read" ./cmd/washnet-read )
( cd "$ROOT_DIR" && CGO_ENABLED=0 go build -o "$OUT/owrt-files/usr/bin/washnet-apply" ./cmd/washnet-apply )
chmod +x "$OUT/owrt-files/usr/bin/"*

# WASH_GUI=1 also bakes the full wash desktop (static multicall) so the VM can
# serve the wash GUI itself — netd autodetects UCI, so the router screens are real.
# Needs the FE asset embeds built first (`make multicall`).
if [ "${WASH_GUI:-}" = "1" ]; then
	echo ">> bake wash desktop (static multicall, GUI)"
	( cd "$ROOT_DIR" && CGO_ENABLED=0 go build -tags=multicall,netgo,osusergo -ldflags="-s -w" -o "$OUT/owrt-files/usr/bin/wash" ./cmd/wash )
	chmod +x "$OUT/owrt-files/usr/bin/wash"
fi

echo ">> Image Builder ($IB): make image (FILES=washnet, PACKAGES=$PKGS)"
docker pull -q "$IB" >/dev/null
docker rm -f wash-owrt-ib >/dev/null 2>&1 || true
docker run --name wash-owrt-ib -v "$OUT/owrt-files:/files:ro" "$IB" \
	make image PROFILE=generic FILES=/files PACKAGES="$PKGS"

echo ">> extract the BIOS ext4-combined image"
GZ="openwrt-$VER-x86-64-generic-ext4-combined.img.gz"
docker cp "wash-owrt-ib:/builder/bin/targets/x86/64/$GZ" "$OUT/openwrt.img.gz"
docker rm -f wash-owrt-ib >/dev/null 2>&1 || true
gunzip -f "$OUT/openwrt.img.gz"
qemu-img resize -f raw "$OUT/openwrt.img" 256M   # headroom for apply/leases (sparse)

echo ">> done:"
ls -lh "$OUT/openwrt.img"

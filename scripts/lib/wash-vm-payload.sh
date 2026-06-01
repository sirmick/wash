# shellcheck shell=sh
# Shared wash-vm image payload helpers, sourced by build-vm-image-<distro>.sh
# (docs/NET.md §8.4). The payload is the same idea on every distro (§2.6 — "the
# wash payload is three static binaries, copying files not a build environment"):
# the ctl-plane agent, the setuid-root netd trampoline, and the multicall wash.
# The per-distro scripts own what genuinely differs — package render (apk vs
# dnf), backend integration (NM/polkit vs networkd), init (OpenRC vs systemd),
# and boot packaging (initramfs cpio vs squashfs disk).

# wvm_goarch <ARCH> — kernel arch → GOARCH.
wvm_goarch() { [ "$1" = x86_64 ] && echo amd64 || echo "$1"; }

# wvm_gobuild <ROOT_DIR> <ARCH> <OUT> <cmd-pkg> — one static, no-cgo guest binary.
wvm_gobuild() {
	CGO_ENABLED=0 GOOS=linux GOARCH="$(wvm_goarch "$2")" \
		go build -trimpath -ldflags="-s -w" -o "$3" "$1/$4"
}

# wvm_build_core <BUILD> <ROOT_DIR> <ARCH> — the binaries EVERY image needs: the
# ctl-plane agent and the setuid-root netd trampoline.
wvm_build_core() {
	echo ">> building guest core (agent + rootexec trampoline)"
	wvm_gobuild "$2" "$3" "$1/washvm-agent" "cmd/washvm-agent"
	wvm_gobuild "$2" "$3" "$1/washvm-rootexec" "cmd/washvm-rootexec"
}

# wvm_build_cli <BUILD> <ROOT_DIR> <ARCH> <name>... — extra cmd/<name> CLIs a
# distro wants on the guest (e.g. washnet-apply/-read for the active backend).
wvm_build_cli() {
	BUILD="$1" ROOT_DIR="$2" ARCH="$3"
	shift 3
	for n in "$@"; do
		echo ">> building $n"
		wvm_gobuild "$ROOT_DIR" "$ARCH" "$BUILD/$n" "cmd/$n"
	done
}

# wvm_require_multicall <ROOT_DIR> — the real payload (router + every app,
# embedding the FE bundles) is `make multicall` output; image building is file
# ops, so require it pre-built. Echoes the binary path.
wvm_require_multicall() {
	WASH_BIN="$1/out/wash"
	if [ ! -x "$WASH_BIN" ]; then
		echo "!! $WASH_BIN missing — run 'make multicall' first (builds the FE bundles + static multicall)" >&2
		exit 1
	fi
	echo "$WASH_BIN"
}

# wvm_stage_payload <RFS> <BUILD> <WASH_BIN> — install the agent, the multicall +
# its per-app symlinks, and replace the wash-netd symlink with the setuid-root
# trampoline so the unprivileged wash desktop can spawn netd as root (NET.md §3;
# NM keyfiles / networkd units must be root-owned). The host binary is the same
# arch, so install-symlinks runs on the host to materialize the guest apps dir.
wvm_stage_payload() {
	RFS="$1" BUILD="$2" WASH_BIN="$3"
	install -Dm755 "$BUILD/washvm-agent" "$RFS/sbin/washvm-agent"
	install -Dm755 "$WASH_BIN" "$RFS/usr/lib/wash/wash"
	"$WASH_BIN" install-symlinks "$RFS/usr/lib/wash" >/dev/null
	ln -sf ../lib/wash/wash "$RFS/usr/bin/wash"
	rm -f "$RFS/usr/lib/wash/wash-netd"
	install -m4755 "$BUILD/washvm-rootexec" "$RFS/usr/lib/wash/wash-netd"
}

# wvm_pack_initramfs <RFS> <OUT_GZ> — root-owned cpio (cpio -R 0:0, so reserved
# ids like com.wash.netd are served from a root-owned/non-world-writable binary,
# see registry.go) → gzip. For initramfs-boot distros (Alpine); systemd distros
# use a squashfs disk instead (see the fedora script).
wvm_pack_initramfs() {
	echo ">> packing initramfs (root-owned for the reserved-id trust)"
	( cd "$1" && find . | cpio -o -H newc -R 0:0 2>/dev/null | gzip -9 ) > "$2"
}

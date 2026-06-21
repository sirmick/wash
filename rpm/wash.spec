Name:           wash
Version:        0.9.1
Release:        3%{?dist}
Summary:        Lightweight remote-admin desktop environment

License:        MIT
URL:            https://github.com/sirmick/wash
Source0:        wash_%{version}.tar.xz

# Pre-built static Go binaries — no toolchain needed at build time.
# Disable the usual debuginfo / build-id processing: there's no DWARF
# in a Go static binary and rpmbuild's debuginfo extractor produces
# empty output that some hosts refuse.
%global debug_package %{nil}
%global _build_id_links none
# wash-display carries an intentional rpath (/usr/lib/wash) to find its
# bundled private libwlroots.so; Fedora's check-rpaths would otherwise fail
# the build on it.
%global __brp_check_rpaths %{nil}
# Arch is selected by the caller via `rpmbuild --target <arch>` (x86_64,
# aarch64, riscv64) so one spec serves every target; the matching prebuilt
# binaries are staged under out/ by the build container. No BuildArch pin —
# that would hard-wire x86_64 and block cross-arch builds.

# The core package ships only the router + app binaries; it creates no user and
# needs no capabilities, so it carries no shadow-utils/libcap requires (those
# move to the wash-login subpackage). systemd-rpm-macros is needed at build time
# for the wash-login subpackage's %systemd_* scriptlets.
BuildRequires:  systemd-rpm-macros

# Optional native display subpackage. Off by default; build with
# `rpmbuild --with display` on a host that built out/wash-display
# (make WASH_DISPLAY=1). Mirrors the Makefile gate.
%bcond_with display

%description
wash is a transport-only router and a small family of web-component
apps that present a Windows/GNOME/macOS-style desktop over HTTP/WS.
Designed for maintaining Linux machines over the network without
needing SSH plus a stack of text-only tools. wash-router runs as the
invoking user (single-user mode); install wash-login for multi-user access.

%package login
Summary:        Multi-user login front door for wash
Requires:       wash = %{version}-%{release}
Requires:       shadow-utils
Requires:       libcap
%{?systemd_requires}
%description login
wash-login is the authenticated, multi-user entry point to a wash desktop:
it presents a login page on 0.0.0.0:10000 and, per session, switches to the
target user's uid and spawns a wash-router for them. Installed as an enabled
systemd service running as the unprivileged wash-system user (created by this
package), granted only the capabilities it needs to switch users (cap_setuid,
cap_setgid, cap_kill) and membership in group shadow for the default
/etc/shadow auth backend. Tunables live in /etc/default/wash-login.

%if %{with display}
%package display
Summary:        Native X/Wayland app surfaces for wash
Requires:       wash = %{version}-%{release}
Requires:       xorg-x11-server-Xwayland
%description display
wash-display runs native Linux GUI apps (Wayland and X11 via Xwayland)
as first-class wash windows: each app surface is captured, encoded, and
streamed to the browser shell. Split out because it links the wlroots
compositor stack the pure-Go core deliberately avoids.
%endif

%prep
%autosetup -n wash-%{version}

%build
# Source tarball already ships prebuilt static binaries under out/.

%install
# Multicall layout: install the dispatcher (out/wash) once + a relative symlink
# per wash-<app> from the canonical name list (packaging/wash.binaries, the CI
# drift-guard vs BINS). wash-sudo is a real standalone helper; wash-login ships
# in its own subpackage (file-caps can't be shared); wash-display is the cgo
# subpackage. Emit the matching %%files list as we go.
install -d %{buildroot}%{_bindir}
install -m 0755 out/wash      %{buildroot}%{_bindir}/wash
install -m 0755 out/wash-sudo %{buildroot}%{_bindir}/wash-sudo
: > wash.files
echo "%{_bindir}/wash" >> wash.files
echo "%{_bindir}/wash-sudo" >> wash.files
while read -r bin; do
    [ -n "$bin" ] || continue
    case "$bin" in wash-login|wash-sudo|wash-display) continue ;; esac
    ln -sf wash %{buildroot}%{_bindir}/$bin
    echo "%{_bindir}/$bin" >> wash.files
done < packaging/wash.binaries
%if %{with display}
install -m 0755 out/wash-display %{buildroot}%{_bindir}/wash-display
# Bundled, private libwlroots.so (vendored 0.17.4). Lives in /usr/lib/wash
# (NOT %%{_libdir}, which is /usr/lib64 on Fedora) to match wash-display's
# rpath, so the layout is identical across distros. rpm's auto-deps emit a
# Provides for its soname, which satisfies the binary's auto-Require.
install -d -m 0755 %{buildroot}/usr/lib/wash
install -m 0755 out/lib/libwlroots.so.* %{buildroot}/usr/lib/wash/
%endif
# wash-login subpackage payload: the front-door binary, its systemd unit
# (single-source packaging/wash-login.service, shared verbatim with debian/),
# its EnvironmentFile, and the /etc/wash dir it writes secret.key into.
install -m 0755 out/wash-login %{buildroot}%{_bindir}/wash-login
install -D -m0644 packaging/wash-login.service %{buildroot}%{_unitdir}/wash-login.service
install -D -m0644 packaging/wash-login.default %{buildroot}%{_sysconfdir}/default/wash-login
install -d %{buildroot}%{_sysconfdir}/wash

%files -f wash.files

%files login
%{_bindir}/wash-login
%{_unitdir}/wash-login.service
%config(noreplace) %{_sysconfdir}/default/wash-login
%attr(0750, wash-system, wash) %dir %{_sysconfdir}/wash

%if %{with display}
%files display
%{_bindir}/wash-display
%dir /usr/lib/wash
/usr/lib/wash/libwlroots.so.*
%endif

# ---- core wash scriptlets -------------------------------------------------
%post
# wash-priv reserved-id trust gate wants root:root 0755. wash-priv is a symlink
# to the multicall binary and the check stat()s through it, so the anchor that
# must be root:root 0755 is %{_bindir}/wash itself. (The %%files mode already
# declares it; be explicit in case the extractor preserved a different mode.)
if [ -e %{_bindir}/wash ]; then
    chown root:root %{_bindir}/wash
    chmod 0755 %{_bindir}/wash
fi
exit 0

# ---- wash-login subpackage scriptlets -------------------------------------
%pre login
# Create the wash group + unprivileged wash-system user before the files land
# so %attr(...,wash-system,wash) /etc/wash resolves. Idempotent.
getent group wash >/dev/null || groupadd --system wash
getent passwd wash-system >/dev/null || \
    useradd --system --gid wash \
            --home-dir /var/lib/wash \
            --shell /sbin/nologin \
            wash-system
exit 0

%post login
# Switching to the target user's uid/gid and reaping the session needs
# cap_setuid, cap_setgid and cap_kill (Makefile wash-login-caps /
# docs/MULTIUSER.md). Grant them so a fresh install is multi-user-ready.
# Best-effort: setcap fails on filesystems without xattr support — warn, don't
# fail the transaction, since single-user (login uid == target uid) needs no caps.
if [ -x %{_bindir}/wash-login ]; then
    if ! setcap 'cap_setuid,cap_setgid,cap_kill+ep' %{_bindir}/wash-login 2>/dev/null; then
        echo "wash-login: warning: could not setcap (no xattr support?);" >&2
        echo "wash-login:   run: setcap 'cap_setuid,cap_setgid,cap_kill+ep' %{_bindir}/wash-login" >&2
    fi
fi
# The default auth backend reads /etc/shadow; group `shadow` grants that without
# running wash-login as root (the unit also sets SupplementaryGroups=shadow, but
# add it at the system level too for least privilege). Best-effort.
if getent group shadow >/dev/null; then
    usermod -aG shadow wash-system >/dev/null 2>&1 || true
fi
%systemd_post wash-login.service

%preun login
%systemd_preun wash-login.service

%postun login
%systemd_postun_with_restart wash-login.service
# Only on full uninstall (not upgrade) of wash-login clean up its user/group/dir.
if [ "$1" = "0" ]; then
    if getent passwd wash-system >/dev/null; then
        userdel wash-system >/dev/null 2>&1 || true
    fi
    if getent group wash >/dev/null; then
        groupdel wash >/dev/null 2>&1 || true
    fi
    rm -rf %{_sysconfdir}/wash
fi
exit 0

%changelog
* Fri Jun 19 2026 sirmick <sirmick@gmail.com> - 0.9.1-3
- wash-login: least-privilege per-session runtime dirs — the setuid'd
  wash-router (granted group wash as a supplementary group) creates its own
  /run/wash/<uid>/sessions/ + socket; wash-login no longer chowns (it has no
  CAP_CHOWN). Runtime root is 2770 group wash (setgid) so ownership/group
  propagate without a privileged chown. wash-router gains --log-file.
* Fri Jun 19 2026 sirmick <sirmick@gmail.com> - 0.9.1-2
- Split the multi-user front door into a wash-login subpackage (Requires: wash)
  that owns the wash-system user, the capabilities, and the systemd service;
  the core wash package is now just the router + apps (no user/caps/service).
- wash-login: enabled systemd unit, /etc/default/wash-login, /etc/wash owned
  wash-system:wash 0750, wash-system added to group shadow — reachable on
  0.0.0.0:10000 OOTB (parity with the deb).
* Tue Jun 16 2026 sirmick <sirmick@gmail.com> - 0.9.1-1
- wash-display: native X/Wayland compositor (guests run as wash windows).
- wash-login: setcap the multi-user front door at install (cap_setuid,
  cap_setgid, cap_kill) so a fresh package is multi-user-ready.
* Thu May 28 2026 sirmick <sirmick@gmail.com> - 0.9.0-1
- Initial packaging.

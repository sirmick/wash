Name:           wash
Version:        0.8.0
Release:        1%{?dist}
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
BuildArch:      x86_64

Requires:       shadow-utils
Requires:       libcap

# Optional native display subpackage. Off by default; build with
# `rpmbuild --with display` on a host that built out/wash-display
# (make WASH_DISPLAY=1). Mirrors the Makefile gate.
%bcond_with display

%description
wash is a transport-only router and a small family of web-component
apps that present a Windows/GNOME/macOS-style desktop over HTTP/WS.
Designed for maintaining Linux machines over the network without
needing SSH plus a stack of text-only tools.

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
install -d %{buildroot}%{_bindir}
for bin in wash-router wash-login wash-session wash-about wash-fm wash-term \
           wash-edit wash-bulk wash-priv wash-journal wash-notify \
           wash-settings wash-top wash-disks wash-syslogs wash-services wash-packages \
           wash-launch wash-sudo; do
    install -m 0755 out/$bin %{buildroot}%{_bindir}/$bin
done
%if %{with display}
install -m 0755 out/wash-display %{buildroot}%{_bindir}/wash-display
%endif
install -d -m 0755 %{buildroot}%{_sysconfdir}/wash

%files
%{_bindir}/wash-router
%{_bindir}/wash-login
%{_bindir}/wash-session
%{_bindir}/wash-about
%{_bindir}/wash-fm
%{_bindir}/wash-term
%{_bindir}/wash-edit
%{_bindir}/wash-bulk
%{_bindir}/wash-priv
%{_bindir}/wash-journal
%{_bindir}/wash-notify
%{_bindir}/wash-settings
%{_bindir}/wash-top
%{_bindir}/wash-disks
%{_bindir}/wash-syslogs
%{_bindir}/wash-services
%{_bindir}/wash-packages
%{_bindir}/wash-launch
%{_bindir}/wash-sudo
%dir %{_sysconfdir}/wash

%if %{with display}
%files display
%{_bindir}/wash-display
%endif

%pre
# Create wash-system uid + wash group before the files land so
# %files ownership claims would be checked correctly. Idempotent.
getent group wash >/dev/null || groupadd --system wash
getent passwd wash-system >/dev/null || \
    useradd --system --gid wash \
            --home-dir /var/lib/wash \
            --shell /sbin/nologin \
            wash-system
exit 0

%post
# wash-priv reserved-id trust gate wants root:root 0755 — files
# section already declares 0755 but be explicit in case the rpm
# extractor preserved a different mode.
if [ -x %{_bindir}/wash-priv ]; then
    chown root:root %{_bindir}/wash-priv
    chmod 0755 %{_bindir}/wash-priv
fi
exit 0

%postun
# Only on full uninstall (not upgrade) clean up the user/group.
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
* Thu May 28 2026 wash maintainers <wash@example.invalid> - 0.8.0-1
- Initial packaging.

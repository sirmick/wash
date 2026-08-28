Name:           wash
Version:        0.14.1
Release:        1%{?dist}
Summary:        Lightweight remote-admin desktop environment

License:        AGPL-3.0-or-later
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
# Example supervisord program for no-systemd/container hosts (docs only).
install -D -m0644 packaging/wash-login.supervisord.conf %{buildroot}%{_datadir}/wash-login/supervisord.conf
install -d %{buildroot}%{_sysconfdir}/wash
# Man pages: source wash.1 + a .so redirect stub per applet (rpmlint wants a
# man page per binary). Bash completion for the dispatcher. Appended to wash.files.
install -D -m0644 packaging/wash.1 %{buildroot}%{_mandir}/man1/wash.1
echo "%{_mandir}/man1/wash.1*" >> wash.files
while read -r bin; do
    [ -n "$bin" ] || continue
    case "$bin" in wash-login|wash-display) continue ;; esac
    echo ".so man1/wash.1" > %{buildroot}%{_mandir}/man1/$bin.1
    echo "%{_mandir}/man1/$bin.1*" >> wash.files
done < packaging/wash.binaries
install -D -m0644 packaging/wash.bash %{buildroot}%{_datadir}/bash-completion/completions/wash
echo "%{_datadir}/bash-completion/completions/wash" >> wash.files
# wash-login subpackage man stub (.so → wash.1 from the wash package, Requires wash).
install -d %{buildroot}%{_mandir}/man1
echo ".so man1/wash.1" > %{buildroot}%{_mandir}/man1/wash-login.1

%files -f wash.files
%license LICENSE

%files login
%{_bindir}/wash-login
%{_mandir}/man1/wash-login.1*
%{_unitdir}/wash-login.service
%{_datadir}/wash-login/supervisord.conf
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
* Wed Aug 27 2026 sirmick <sirmick@gmail.com> - 0.14.1-1
- agent: a failed session renders red instead of green. A turn that died on
  an adapter error reported as done, and done paints green, so failure was
  indistinguishable from success on every screen wash has.
- agent: the rail no longer counts stale and finished sessions as working.
- agent: the transcript stream rides Bulk, so a talking agent stops competing
  with window moves and keystrokes for the browser's single socket; the two
  panes scroll independently instead of sharing one scrollbar.
- router: helper binaries decline the control-socket probe instead of printing
  their usage at boot.
- packages: search works on Fedora 41+. dnf5 replaced the " : " field
  separator with a TAB and the parser silently dropped every result.
* Mon Aug 24 2026 sirmick <sirmick@gmail.com> - 0.14.0-1
- agent: every door (sidebar, roster row, notification) goes TO the session
  that wants you, opening a window only when there is none, instead of a new
  Agent window per click. A blocked agent toasts its question and the click
  lands on that session.
- agent: a waiting question marks its taskbar pill; looking at the window
  clears it, and the router owns that so no app can leave it blinking.
- agent: the sessions list is always on screen and resizable (width
  remembered per window), replacing a toggle with three opening rules.
- agent: full-text history search — all words required, any order, the
  matching line shown with terms marked, trigram-indexed so it stays instant.
- agent: the new-session form opens on the agent and folder you used last.
- chrome: a menu closes when the window under it moves.
- router: a router no longer takes a control socket another router is still
  answering on; the loser's apps used to reach the wrong router and fail to
  launch.
- router: a session outlives the browser that opened it and comes back whole.
- sidebar: the right rail is host-aware — counts, questions and doors span
  every connected host, with the verbs moved into the apps.
* Sat Aug 08 2026 sirmick <sirmick@gmail.com> - 0.13.2-1
- term: a terminal keeps following its own output after a heavy burst. The
  resync reset the xterm out of band, jumping xterm's async parse queue and
  detaching the viewport; it now travels in band with the data.
- agent: a finished session no longer sits on "working..." until the next turn
  (late notifications were re-marking it busy after the turn had ended).
- agent: a command run, read and released in quick succession keeps its output
  in the transcript (release dropped the record before the pty could complete
  the entry).
- router: reconnect replay robustness — truncated replays cut through the first
  newline, failed resyncs retry via the watchdog, video channels get a fresh
  frame instead of a corrupting ring replay.

* Fri Aug 07 2026 sirmick <sirmick@gmail.com> - 0.13.1-1
- remote-fs: recover from a silent SSH link death on both the mount data path
  and the shared watch channel (NAT/conntrack drop, suspend, cable pull now
  reconnect instead of wedging).
- remote: fix a data race on the wash-remote supervisor's published host/mount
  state (copy-on-write the status slices).
- wash-vm: the in-browser WASM VM inflates the gzip shell bundle so the desktop
  mounts again; adds a headless boot gate (make browser-vm-test).

* Fri Aug 07 2026 sirmick <sirmick@gmail.com> - 0.13.0-1
- agent: ACP filesystem and terminal capabilities. An agent that opts in reads
  and writes through wash, confined to its session folder, and hands wash its
  shell commands — which run as real ptys you can watch and interrupt.
- agent: wash-edit hosts agent sessions in its terminal pane, in the folder the
  editor has open, with tool rows opening files in the buffer above.
- agent: per-session host-side auto-approval (yolo), badged and recorded in the
  transcript, unable to reverse an explicit deny.
- terminal: closing a tab or window asks first and names what is running;
  per-pane status bars.
- files: a symlink to a folder behaves as a folder in pickers, trees and fm.
- test: the three long-standing e2e failures are fixed; the push gate gates.
* Thu Aug 06 2026 sirmick <sirmick@gmail.com> - 0.12.1-1
- shell: a connection lost before the desktop finished painting no longer hides
  behind the boot splash; the splash stands down on connection loss and the
  connection banner outranks it, so "Reconnect now" is reachable.
- router: the env.publish log line names the keys it accepted, not just a count.
- test: the three long-standing e2e failures (reconnect + the two display
  capstones) were real defects and are fixed, along with a false-pass in the
  terminal burst soak; the e2e gate blocks pushes honestly again.
* Thu Aug 06 2026 sirmick <sirmick@gmail.com> - 0.12.0-1
- agent: wash runs coding agents itself over the Agent Client Protocol; new
  Agent app (com.wash.ai) with a launcher, a streaming transcript, markdown,
  tables and inline images.
- agent: permission requests arrive as structured data and land in the same
  approval queue the sidebar already rendered; answer from either view.
- agent: the agent's own settings on the window — approval mode, model,
  reasoning effort, plan mode — so an org's pinned policy still holds.
- agent: stop a turn, see context used, slash-command completion, and
  sessions named by the title the agent writes itself.
- agent: removed the previous hook/OSC/decision-socket mechanism and the
  wash-agent-hook helper.
- ui: shared MenuBar in @wash/ui.
- term: restored TERM / $WASH_BIN_DIR / display-hint env for terminals.
* Sun Aug 02 2026 sirmick <sirmick@gmail.com> - 0.11.0-1
- term: agent-aware terminals — tab state dot + status line driven by an OSC
  7770 status channel and a foreground-process check.
- term: transition toasts (needs-input / finished) with click-to-focus and a
  taskbar attention dot.
- term: agent approval policy over a per-pty decision socket, edited in
  Settings -> Agents; off by default, fails open to the agent's own prompt.
- agentd: new com.wash.agentd roster service — one sidebar row per agent,
  answer permission requests inline, "always allow" writes the rule.
- agentd: session history with Resume / Fork after a reboot or closed window.
- term: smart paste — silent repair of invisible junk, preview before a
  wrapped command is rejoined, paste-jacking warning.
- router: scrollback ring grows to 4 MiB while detached (20,000 lines kept
  client-side) so output written with the lid shut survives.
- term: content no longer paints over the scrollbar; the bar is themed.
- cli: wash-agent-hook + `wash agent-hooks install|remove|status`.
* Thu Jul 09 2026 sirmick <sirmick@gmail.com> - 0.10.0-1
- clipboard: converge the wash + system clipboards behind HTTPS (pastes
  prefer readText where readable, wash-clipboard fallback; native pastes +
  field copies mirror in; BE-originated changes mirror out).
- term: right-click copies the selection / pastes when there is none;
  Shift+right-click keeps the Copy/Paste menu.
- fm: context menu gains Cut / Copy / Paste for files.
- ui: context menus clamp into the viewport (no off-screen items).
- net/netd/edit/crash-pane copy buttons ride the shared clipboard helpers.
- top: process rows are text-selectable.
* Wed Jul 08 2026 sirmick <sirmick@gmail.com> - 0.9.7-1
- router: disable HTTP/2 on the HTTPS fronts (h2 has no Hijacker; broke the
  vscode /app/ ingress + /ws fd-handoff: "hijacker not supported").
- remote: serve a peer's /app/ ingress through the local origin (issue 15);
  new --listen-ingress/--peer-ingress; remote vscode/music/radio/washamp work.
- router: redirect plain-HTTP hits on the HTTPS listener to https://.
* Mon Jun 29 2026 sirmick <sirmick@gmail.com> - 0.9.6-4
- radio: load stations from a user-editable ~/.config/wash/radio-stations.json
  seeded from embedded defaults; refreshed SomaFM set + hacker/cyberpunk adds;
  Ambient vs Electronic genre taxonomy cleanup; tree opens collapsed except the
  last-played station path.
- router: join in-flight ctl-listener handoffs on shutdown and CloseNow() a
  cancelled session's WebSocket, fixing a make test-race data race and a 5s
  shutdown stall.
- dev: add a non-root local toolchain installer (Go, Node.js/npm, pnpm).

* Mon Jun 29 2026 sirmick <sirmick@gmail.com> - 0.9.6-3
- UI: consolidate every app frontend onto the shared @wash/ui design language
  (Button/Input/Checkbox + tokens); data-viz palettes onto themeable accents;
  fix white-on-accent buttons invisible on light packs; add make check-design.
- conn: WebSocket heartbeat + post-sleep wake recovery + disconnect diagnostics,
  with a full-stack reconnect/banner e2e and SO_REUSEADDR.
- radio: broad nested genre/subtype station tree + stream details panel.

* Thu Jun 25 2026 sirmick <sirmick@gmail.com> - 0.9.6-2
- wash-display: HiDPI display scaling, virtual output sized from shell metrics,
  guest-surface activation on wash focus, Xwayland popup pointer grabs,
  link-bitrate tracking + display framerate in About, Settings status polling.
- Render Qt menu-fallback toplevels as popover overlays.
- theme: net/services idiom, UI mono cleanup, Copland vintage wallpaper,
  non-overlapping Dreamtime dots, terminal inset, themeable menu effect/shadow.
- fix(session): don't let the clipboard widget's mount-time seed clobber a newer
  paste.
- fix(router): order bulk-download completion + teardown behind the bytes.

* Wed Jun 24 2026 sirmick <sirmick@gmail.com> - 0.9.6-1
- Theme packs: new Dreamtime (dot-painting) + Copland packs; per-theme
  wallpaper extent/border, configurable taskbar backdrop, bundled fonts.
- Terminal: more themes + live palette switching, bundled monospace fonts,
  per-tab user badge/status line, OSC tab titles.
- Link health: WS link.stats telemetry, gzip+cache on asset/bundle delivery,
  QoS tc reclassification.
- Shared <FileTree> in @wash/ui adopted by fm + edit; BE-owned view-state
  persistence helper.
- File manager: folder-granular external upload, lossless Bulk download.
- wash-priv standing per-app grant; remote nesting forbidden; shell boot splash.

* Wed Jun 24 2026 sirmick <sirmick@gmail.com> - 0.9.5-2
- Package revision bump (no change to installed binaries). Dev build-layout
  reorg: multicall builds into out/, standalone into out/singlecall/, plus a
  make wash-display verb. package-tree output is unchanged — functionally
  identical to 0.9.5-1, rebuilt for a clean estate re-rollout.

* Tue Jun 23 2026 sirmick <sirmick@gmail.com> - 0.9.5-1
- build: multicall is now the default dev layout (make wash / run / dev); the
  per-app binaries build via make wash-standalone.
- router/wire: app-probe rejects --wash-manifest cleanly on non-app binaries;
  the "no header newline" diagnostic now reports byte count + snippet, captured
  stderr, and the binary path in the disable reason.
- notify: fix a StateService snapshot data race; add a -race CI gate.
- wash-vm: control plane is a single-owner actor (no lock held across I/O).

* Mon Jun 22 2026 sirmick <sirmick@gmail.com> - 0.9.4-1
- remote: LAN mDNS auto-discovery ("On your network") for wash-connect plus a
  Settings "Remote" panel listing live sessions + mounts with graceful teardown.
- theme: Copland pack, sunwave logo, dead-black terminal/editor, denser start
  menu, host info in the start header.
- wash-login: ship an example supervisord program at
  /usr/share/wash-login/supervisord.conf (docs only, not activated) for
  no-systemd/container hosts; reads the same /etc/default/wash-login args.

* Mon Jun 22 2026 sirmick <sirmick@gmail.com> - 0.9.3-1
- build: a single app roster drives the per-app Makefile rules, the multicall
  imports (make gen-imports), and a manifest-icon sprite check (CORE_AUDIT 2.2).
- Sync the rpm package version to 0.9.3 (it had been stranded at 0.9.1 while
  deb/binaries moved to 0.9.2).

* Sat Jun 20 2026 sirmick <sirmick@gmail.com> - 0.9.1-4
- Ship the multi-call binary by default: one /usr/bin/wash + relative wash-<app>
  symlinks (installed 118M -> 21M). wash-login/wash-sudo stay real binaries.
- Man pages per applet (.so stubs) + bash completion; %license LICENSE.
- Fix license metadata to AGPL-3.0-or-later (was wrongly MIT). -buildvcs=false.
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

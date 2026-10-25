# wash site: copy for review (respin for 0.19)

Every user-visible string on `site/index.html`, in page order. Edit freely and I'll carry the changes back into the HTML.

**What this respin changes** (marked **✎** in the copy below; unmarked lines are unchanged from the first draft):

1. **Window layouts.** The hero line mentions them, and section 01 gets a new block, *Tile them into one IDE*, with a new figure. Tiling is what makes "four apps as one IDE" literally true, so it goes in the IDE section and not under the desktop.
2. **Nine themes, up from six.** Lisbon, Isfahan and Moscow join. The themes paragraph no longer says every wallpaper is vector art, because Isfahan and Moscow ship as WebP paintings. It also now gives examples of themes changing shape and not just colour.
3. **Screenshot captions follow the reshoot.** Files and Services are now shot under Lisbon, Top and Radio under Isfahan, and Music under Moscow.
4. **The live demo** says that it carries only two of the themes.
5. **Remote apps** shows a real cross-machine session (`remote.png`) instead of the Connect dialog, which moves to the gallery.

> **Reviewer notes** (not copy):
> - **Done:** `ide-tiled.png` is a new capture (`make screenshots`, scene `ide-tiled`). It's Editor, Agent and Terminal on a seeded Go project, tiled `c[r2@64 1]` with the real `setCellLayout`. Thumbnails were regenerated for all nine packs, so Copland's is current.
> - **`remote.png`** is a real capture (`make screenshots`, scene `remote`). `pi-garage` is an arm64 Debian container (`e2e/capture/remote-host/`, run under qemu-user) with sshd and the arm64 wash build. Connect reaches it over real SSH, through a throwaway key and config. The scene needs docker and `make OUT=out-arm64 GOARCH=arm64 multicall-bin`, and skips without them.
> - **Option, not drafted:** make the tiled IDE the hero figure (Fig. 01) and move the floating montage down to section 02.
> - **Demo numbers re-measured** on the 0.19 VM build: ~11 MiB (kernel 2.1 MiB and rootfs 8.0 MiB gzipped, plus the page and wasm; every rootfs chunk is prefetched at boot) and a 16 MB riscv64 multicall binary that links only the 11 apps the VM installs.
> - **Privacy:** `top.png`, from the theme-pack reshoot on `main`, shows real processes from the capture box, including paths under `/home/mick`. That needs a reshoot with a cleaner process list, or a filter, before going public.

---

- IDE

- Desktop

- How it works

- Gallery

- Install

- Live demo

- GitHub

[label] A Linux desktop environment for the browser Go · one WebSocket · rendered as HTML

# Terminal, editor, files and agent, as one IDE in your browser

**✎** wash is a Linux desktop in your browser. Its four core apps share folders, links and a clipboard, and tile side by side like an IDE. It runs from one Go binary on a laptop, a home server or a Raspberry Pi.

[button] Try it live ↓

[button] How it fits together ↓

[button] GitHub ↗

[image: desktop-montage.png] A wash desktop: a Washamp audio player, a Terminal, the Files window and an Agent session open over the wallpaper, with the system sidebar on the right and a taskbar along the bottom.

[caption] Fig. 01A real session from `make screenshots` (agent replies scripted). Windows are HTML drawn by your browser; only events and file data cross the network.

- **Server**

  A single static Go binary

- **Connection**

  One WebSocket carrying events and file data, no video

- **Footprint**

  ~25 MB idle, ~100 MB for a full session

- **Runs on**

  amd64 · arm64 · riscv64 · mips

- **License**

  AGPL-3.0

[label] 01The IDE

## How the four apps connect

Each app has menu-bar buttons that open the other three in the same folder. Windows opened that way share a tint, and pressing the button again brings that window back instead of opening a new one.

[image: fm.png] Files: a tree and preview pane over a project folder.

Files

Previews and live updates. Double-click a file to edit it, or drag it into an agent session as context.

[image: term.png] Terminal: a tabbed shell session.

Terminal

**✎** Real shells with tabs and splits. `wash-edit main.go` or `xdg-open` opens a file in the editor; links, copies and notifications reach the desktop.

[image: edit.png] Editor: CodeMirror with a file tree, editing hello.go.

Editor

CodeMirror 6 with a file tree, tabs and its own terminal. Reloads when an agent or shell changes the file.

[image: agent.png] The Agent window: a transcript with a code block, bullet points and a results table, after the agent fixed a flaky test.

Agent

Claude Code, Codex, OpenCode or Gemini over ACP. It stays inside the project folder, runs commands in terminals you can watch, and its file links open in the editor.

Agent sessions keep running when you close the window and stay on the sidebar. The Agents manager starts new ones or resumes old ones from history.

**✎ [label] Layouts**

**✎ ### Tile them into one IDE**

**✎** Pick a layout from the Layout menu, such as halves, thirds, 2×2 or main + stack, and the windows on that desktop snap into panels. Drag a divider to resize panels, drop a window on a panel to swap it in, or press Ctrl+Alt+arrows to move between panels. Desktops without a layout stay floating.

**✎** Layouts update live. Closing a window leaves its panel empty for the next one, and opening the sidebar or resizing the browser refits the panels. The layout is saved on the server, so when you press **Use here** on your laptop, the panels refit to the laptop's screen.

**✎ [image: ide-tiled.png]** Editor, Agent and Terminal tiled on one desktop: the editor top left, the agent top right and the terminal across the bottom, with the sidebar's pager showing the layout in miniature.

**✎ [caption]** Fig. 02 The editor and agent side by side, a terminal across the bottom. The sidebar's pager draws the layout in miniature.

[label] 02The desktop

## The rest of the desktop

**✎** A taskbar, launcher, floating or tiled windows, workspaces and a status sidebar. The window manager runs in the browser; each app is a small program the server starts and supervises.

[label] Themes

### Everything is themeable

**✎** The look is CSS and art: colours are CSS variables; icons and the start button are vector art that stays crisp at any zoom; wallpapers are SVG, or paintings wide enough for an ultrawide. A theme can change shape as well as colour. Moscow has square corners and hard poster shadows, Lisbon has cut-tile corners, and Copland has a raised Platinum bevel. Switching theme in Settings restyles every open window at once. Nine themes are included, five dark and four light.

- Midnight

- Tokyo

- Seoul

- Copland

- Oslo

- Dreamtime

- **✎** Lisbon

- **✎** Isfahan

- **✎** Moscow

[image: top.png] System Monitor: live CPU, memory and disk meters above a process table.

[label] System tools

### Admin tools for the real machine

Apps run as your Unix user and call the system directly. Root actions are approved in the browser, the password never touches a terminal, and root windows get a red stripe.

System Monitor

CPU, memory, network and disk from `/proc`, plus per-process signals.

Services & logs

systemd, OpenRC or procd units, with journald and `/var/log` viewers.

Packages

apt, dnf or apk, with output streamed into a terminal.

Disks & Network

Partitions, RAID, LVM, btrfs, ZFS, SMART. Network changes are verified and roll back if they break connectivity.

[image: services.png] Services: a list of systemd units with start, restart and enable controls.

**✎** [caption] Services, Lisbon theme.

[label] Display

### X11 and Wayland apps in wash windows

Launch a graphical program from a wash terminal and the bundled `wash-display` compositor gives it a wash window, like Chromium and `xclock` here.

[image: display.png] Chromium and xclock, launched from a wash terminal, shown as native wash windows on the desktop.

[label] Remote apps

### Apps from your other machines

**✎** Type `user@host` into Connect to open that machine's apps next to your local ones, striped and labelled by host. It runs over SSH only, inside the connection your browser already has, with one taskbar and clipboard across all of them. The other machine needs nothing but wash and an SSH login.

**✎ [image: remote.png]** A wash desktop on an x86_64 machine with four tiled windows: a local terminal, and a terminal, Editor and Files from pi-garage, an arm64 machine, each striped and labelled @pi-garage.

**✎ [caption]** Fig. 03 Top left is this machine (`x86_64`). The other three windows are apps running on `pi-garage`, an arm64 host reached over SSH. Running `wash-edit` in its terminal opened its Editor here.

[label] 03How it works

## Your browser renders every window

There's no video stream like VNC or RDP. Windows are HTML, so text stays sharp at any zoom and the desktop stays fast on slow links.

On the server, a router starts app processes and relays their messages without reading them. Bandwidth follows what you do, not your screen size.

### Ships as one binary

The Go code builds with `CGO_ENABLED=0`, so it is static and cross-compiles easily. Shipped as one `wash` executable with `wash-<app>` symlinks, the whole desktop is one file.

Each app embeds its web UI, so apps are single files too. The Makefile also builds deb, rpm and apk packages.

- Browserwindow manager · shell · app UIs

- Wireone WebSocket · prioritized channels

- Routersupervise · mux · never parse

- Appsone process each · capabilities

- Linuxyour user · real syscalls

[caption] One connection, end to end

### Each app is its own process

The router starts and supervises apps. They can't talk directly: messages go through the router, which vouches for the sender, and permissions are granted per app.

For several users, `wash-login` uses the system login (PAM, LDAP, SSSD), starts a router per user under their account, and hands the connection over with `SCM_RIGHTS`.

- **~25 MB**

  resident for an idle router, no shell connected

- **7–10 MB**

  for a typical app backend process

- **~100 MB**

  for a full session: router plus a dozen apps

- **7.5 MB**

  router binary on disk (amd64, static, stripped)

[label] 04Gallery

## More of the desktop

[image: agents.png] The Agents manager: start a session from a catalog and model, with Running and History lists

Agents start from a catalog, resume from history Midnight

[image: washamp.png] Washamp, a Winamp-skinned audio player

Washamp Webamp with the classic skins Midnight

[image: music.png] Music, a native player

**✎** Music one folder, recursive list Moscow

[image: radio.png] Radio, internet radio stations

**✎** Radio SomaFM, Radio Browser, ICY metadata Isfahan

[image: imageview.png] Image Viewer with a thumbnail list

Image Viewer bytes over the wire, zoom and pan Seoul

[image: settings.png] Settings with the theme gallery

Settings themes, wallpaper, clock, taskbar Seoul

[image: packages.png] Packages, a package manager front end

Packages apt, dnf, apk Copland

[image: disks.png] Disks, a storage manager

Disks md, LVM, btrfs, ZFS, SMART Oslo

[image: net.png] Network, with interfaces and a VLAN switch matrix

Network VLAN switch, firewall, rollback Oslo

**✎ [image: connect.png]** Connect: an SSH target field and a list of hosts discovered on the network

**✎** Connect SSH targets, hosts found on the network Copland

[image: about.png] About wash, with build facts and a process table

About build, host, live process table Tokyo

[label] 05Install

## Build or install it

[label] From source

### Build and run

Needs Go ≥ 1.25, pnpm and make. Open the URL it prints.

[label] On a home server

### Run it as a service

Runs as a `systemd --user` service from `~/wash`. Run it again to update.

[label] Multi-user

### Several users, behind TLS

`wash-login` gives each system user a separate session.

Native deb, rpm and apk packages, router flags and the trust model are in the README.

[label] 06Try it

## Try it here

**✎** While you read, this page booted Linux 6.6 on a RISC-V emulator compiled to WebAssembly, running wash. It's all static files: no backend, no account. Click into the desktop below. To keep the download small it carries two of the nine themes, Midnight and Copland.

[button] Launch the demo →

- **~11MiB**

  downloaded before the desktop is usable

- **64MB**

  of emulated RAM for Linux and the desktop

- **16MB**

  binary with every app, the router and login

- **0**

  servers: static files, and the machine runs in your tab

[button] Open the demo full screen ↗

[button] Star on GitHub ↗

wash (Web Application SHell) is a Linux desktop and IDE that you use in a browser.

[label] Explore

- Live demo

- The desktop

- How it works

- Gallery

[label] Project

- Source

- Install

- Docs

- Issues

[label] About

- AGPL-3.0

- Architecture

- Releases

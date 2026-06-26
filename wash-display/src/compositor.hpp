// wash-display wlroots compositor (commit 7+).
//
// Front-half of docs/DISPLAY.md: a headless wlroots compositor +
// Xwayland (later) that turns every xdg/X11 toplevel into a wash window
// through the wire connection. Compiled only when wlroots is present
// (CMake defines WASH_DISPLAY_COMPOSITOR); main.cpp falls back to the
// fake "display_open" reference path otherwise.
#pragma once

#include <wash/wire_conn.hpp>

namespace wash {

// run_compositor brings up the headless compositor and runs its
// wl_display event loop on the CALLING thread until the socket dies or
// the display terminates. Each mapped toplevel calls
// conn.create_window(); each unmap/destroy calls conn.destroy_window().
// Per-surface capture + video channels arrive in commit 8.
// Returns 0 on clean shutdown, non-zero on setup failure.
int run_compositor(WireConn& conn);

// post_input enqueues one FE input app_msg payload
// ({kind:"input", win, events:[...]}, docs/DISPLAY.md §6) for injection
// into the focused/target surface. Called from the WireConn reader thread
// (main.cpp's app_msg handler); the events are drained and injected on the
// compositor thread via the same self-pipe the resize/close commands use,
// because wlroots is single-threaded. Safe to call before/without a
// running compositor (the payload is simply dropped). The target window is
// the payload's "win", not the app_msg envelope win (cross-instance
// app_msgs arrive on the instance's primary window).
void post_input(const json& data);

// post_display_dpi updates the compositor's advertised virtual-output DPI.
// Like post_input, this is safe from the WireConn reader thread: the actual
// wlroots output mutation is drained on the compositor thread.
void post_display_dpi(int dpi);

// post_display_metrics updates the compositor's advertised virtual-output
// dimensions. Width/height are physical framebuffer pixels; scale is the
// shell-selected HiDPI factor (1 or 2 today) and is published for clients
// that want to map back to CSS-sized wash windows.
void post_display_metrics(int width, int height, int scale);

} // namespace wash

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

} // namespace wash

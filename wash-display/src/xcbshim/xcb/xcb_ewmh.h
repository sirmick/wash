// Minimal local shim for <xcb/xcb_ewmh.h>.
//
// Used ONLY when libxcb-ewmh-dev is not installed (the CMake build adds
// this directory to the include path only in that case). wlr/xwayland.h
// hard-includes <xcb/xcb_ewmh.h> solely for the type
// xcb_ewmh_wm_strut_partial_t, which appears in struct
// wlr_xwayland_surface as a *pointer* field
// (`xcb_ewmh_wm_strut_partial_t *strut_partial`) — so an incomplete type
// is sufficient to compile. We never call any xcb-ewmh function; the EWMH
// symbols wlroots uses are resolved from libwlroots' own link against the
// runtime libxcb-ewmh.so. For a complete, canonical build install
// libxcb-ewmh-dev (already listed in the wash-display package deps,
// docs/DISPLAY.md §8) and this shim is bypassed automatically.
#ifndef WASH_XCB_EWMH_SHIM_H
#define WASH_XCB_EWMH_SHIM_H

#include <xcb/xcb.h>

// Incomplete type — only ever used as a pointer in wlr_xwayland_surface.
typedef struct wash_xcb_ewmh_wm_strut_partial xcb_ewmh_wm_strut_partial_t;

#endif // WASH_XCB_EWMH_SHIM_H

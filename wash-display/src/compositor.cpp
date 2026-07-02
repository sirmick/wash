// wash-display headless wlroots compositor (commit 7).
//
// A minimal wlroots compositor with NO real output device: the headless
// backend is the right fit for a host-native streaming compositor —
// there is no monitor, we capture per-surface buffers and stream them
// (commit 8). This commit only brings up the compositor and maps every
// xdg toplevel onto a wash window via WireConn::create_window().
//
// Structure follows the wlroots reference (tinywl), trimmed to scope:
// no cursor/seat/keyboard (input is commit 9), no Xwayland (commit 10),
// no capture/encode (commit 8). Built only when wlroots is present
// (CMake defines WASH_DISPLAY_COMPOSITOR); targets the system wlroots
// 0.17 API.
#include "compositor.hpp"
#include "capture.hpp"
#include "encode.hpp"

// ALL C++/STL headers MUST be included BEFORE the `#define static` hack
// below — otherwise the macro is in effect while libstdc++ headers parse
// and corrupts them (e.g. <limits>'s `static constexpr` -> ~600 errors).
#include <algorithm>
#include <atomic>
#include <cmath>
#include <csignal>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <ctime>
#include <fcntl.h>
#include <linux/input-event-codes.h>
#include <map>
#include <mutex>
#include <string>
#include <thread>
#include <unistd.h>
#include <unordered_map>
#include <vector>
#include <xkbcommon/xkbcommon.h>

#ifdef WASH_DISPLAY_XWAYLAND
// xcb headers (pulled in transitively by wlr/xwayland.h) carry many
// `static inline` helpers; include them NORMALLY here so the `#define
// static` hack below — needed for wlroots' C99 array-param render headers
// — cannot mangle them. wlr/xwayland.h re-includes these under their own
// include guards, so the copy inside the hacked block is a no-op.
#include <xcb/xcb.h>
#include <xcb/xcb_ewmh.h>
#include <xcb/xcb_icccm.h>
#endif

// WLR_USE_UNSTABLE is defined by the build (CMake). wlroots' headers
// pull in generated protocol headers (xdg-shell-protocol.h) that CMake
// generates with wayland-scanner into the build include dir.
//
// vendored wlroots 0.17.4's render/scene headers declare functions with
// C99 "array with static bound" parameters (e.g. `const float
// color[static 4]`), which is not valid C++ and won't parse under our
// C++ compiler. We only call the scene/xdg API, never these float-array
// render entry points, so neutralize the `static` qualifier for the
// duration of these C headers (0.18 dropped this old render API).
#define static
extern "C" {
#include <wayland-server-core.h>
#include <wlr/backend.h>
#include <wlr/backend/headless.h>
#include <wlr/render/allocator.h>
#include <wlr/render/wlr_renderer.h>
#include <wlr/render/wlr_texture.h>
#include <wlr/types/wlr_compositor.h>
#include <wlr/types/wlr_data_device.h>
#include <wlr/types/wlr_output.h>
#include <wlr/types/wlr_output_layout.h>
#include <wlr/types/wlr_xdg_output_v1.h>
#include <wlr/types/wlr_scene.h>
#include <wlr/types/wlr_subcompositor.h>
#include <wlr/types/wlr_seat.h>
#include <wlr/types/wlr_keyboard.h>
#include <wlr/types/wlr_pointer.h>
#include <wlr/types/wlr_cursor_shape_v1.h>
#include <wlr/interfaces/wlr_keyboard.h>
#include <wlr/types/wlr_xdg_shell.h>
#include <wlr/types/wlr_xdg_decoration_v1.h>
#include <wlr/util/box.h>
#include <wlr/util/log.h>
#ifdef WASH_DISPLAY_XWAYLAND
// wlr/xwayland.h declares a struct field named `class` (valid C, reserved
// in C++). Rename the token for the span of this one header so it parses
// under C++; we never reference that field. (xcb/xcb_ewmh/xcb_icccm were
// already included normally above, so their guards skip re-parsing here.)
#define class class_
#include <wlr/xwayland.h>
#undef class
#endif
}
#undef static

namespace wash {

// Allocator handle the capture path uses to mint per-surface render targets
// (defined here, declared extern in capture.cpp). Set in run_compositor().
struct wlr_allocator* g_capture_allocator = nullptr;

namespace {

// Default virtual screen until the browser shell publishes its viewport. The
// shell sends physical framebuffer pixels (CSS viewport × HiDPI scale), so
// browser-sized windows see a browser-sized display instead of hard-coded
// 1080p.
constexpr int kDefaultScreenW = 1920;
constexpr int kDefaultScreenH = 1080;
constexpr int kMinScreenW = 320;
constexpr int kMinScreenH = 240;
constexpr int kMaxScreenW = 7680;
constexpr int kMaxScreenH = 4320;
constexpr int kDefaultDpi = 96;
constexpr int kMinDpi = 72;
constexpr int kMaxDpi = 240;
constexpr int kDefaultScale = 1;
constexpr int kMaxScale = 2;

static int clamp_dpi(int dpi) {
    if (dpi <= 0) return kDefaultDpi;
    return std::max(kMinDpi, std::min(kMaxDpi, dpi));
}

static int dpi_to_mm(int px, int dpi) {
    return std::max(1, (int)std::lround((double)px * 25.4 / (double)clamp_dpi(dpi)));
}

static std::atomic<int> g_output_dpi{kDefaultDpi};
static std::atomic<int> g_output_w{kDefaultScreenW};
static std::atomic<int> g_output_h{kDefaultScreenH};
static std::atomic<int> g_output_scale{kDefaultScale};

static int clamp_screen_w(int w) {
    if (w <= 0) return kDefaultScreenW;
    return std::max(kMinScreenW, std::min(kMaxScreenW, w));
}

static int clamp_screen_h(int h) {
    if (h <= 0) return kDefaultScreenH;
    return std::max(kMinScreenH, std::min(kMaxScreenH, h));
}

static int clamp_scale(int scale) {
    if (scale <= 0) return kDefaultScale;
    return std::max(kDefaultScale, std::min(kMaxScale, scale));
}

static int output_w() { return g_output_w.load(); }
static int output_h() { return g_output_h.load(); }
static int output_scale() { return g_output_scale.load(); }
static int output_logical_w() { return std::max(1, output_w() / output_scale()); }
static int output_logical_h() { return std::max(1, output_h() / output_scale()); }
static uint32_t physical_to_logical(int px) {
    int scale = output_scale();
    if (scale <= 1) return (uint32_t)std::max(0, px);
    return (uint32_t)std::max(1, (px + scale - 1) / scale);
}

struct Server {
    struct wl_display* display = nullptr;
    struct wlr_backend* backend = nullptr;
    struct wlr_renderer* renderer = nullptr;
    struct wlr_allocator* allocator = nullptr;
    struct wlr_scene* scene = nullptr;
    struct wlr_scene_output_layout* scene_layout = nullptr;
    struct wlr_output_layout* output_layout = nullptr;
    std::vector<struct wlr_output*> outputs;
    struct wlr_xdg_shell* xdg_shell = nullptr;

    struct wl_listener new_output;
    struct wl_listener new_xdg_toplevel;

    // xdg-decoration: force server-side decorations so GTK/Wayland apps
    // drop their own titlebar/shadow — wash draws the real frame,
    // otherwise the client renders a titlebar INSIDE the wash window.
    struct wlr_xdg_decoration_manager_v1* xdg_decoration = nullptr;
    struct wl_listener new_toplevel_decoration;

    // Seat + a single virtual keyboard (commit 9). The seat is required
    // even for display-only X11: Xwayland's core keyboard device fails to
    // initialize (aborting the X server) unless the compositor advertises
    // a seat keyboard with a valid xkb keymap. Pointer input is notified
    // through the same seat. No physical input devices exist (headless);
    // events are injected from app_msg (FE → BE, DISPLAY.md §6).
    struct wlr_seat* seat = nullptr;
    struct wlr_keyboard vkbd;       // virtual keyboard backing the seat
    bool vkbd_inited = false;
    // The virtual keyboard's key/modifier signals drive the seat (tinywl
    // pattern): wlr_keyboard_notify_key updates xkb state and fires these,
    // and we forward to wlr_seat_keyboard_notify_* so the focused client
    // gets keys WITH correct modifier state (shift/ctrl/etc.).
    struct wl_listener vkbd_key;
    struct wl_listener vkbd_modifiers;

    // Clipboard bridge (DISPLAY.md §7). request_set_selection accepts a
    // guest taking the selection; set_selection fires on every change (incl.
    // X11 via the xwm bridge) and mirrors guest→wash. wash_source is the
    // data source we install for wash→guest paste — skipped when we see it
    // come back through set_selection (avoids a copy loop).
    struct wl_listener req_set_selection;
    struct wl_listener set_selection;
    struct wlr_data_source* wash_source = nullptr;

    // cursor-shape-v1: clients name their cursor (text/pointer/resize/…); we
    // forward the NAME to the focused window's element, which sets it as the
    // CSS cursor. Bitmap cursors (request_set_cursor with a surface) are
    // deferred (M4b). DISPLAY.md §12 (M4).
    struct wlr_cursor_shape_manager_v1* cursor_shape_mgr = nullptr;
    struct wl_listener cursor_shape_request;

#ifdef WASH_DISPLAY_XWAYLAND
    struct wlr_xwayland* xwayland = nullptr;
    struct wl_listener new_xwayland_surface;
#endif

    WireConn* conn = nullptr;
};

// WindowSink is the per-window half of the pipeline shared by both
// surface sources (xdg-shell toplevels and, via the Xwayland bridge,
// X11 windows): one wash window id, its video channel, and the pooled
// capture+WebP encoder. Both Toplevel and XSurface embed one so the
// frame path is written once. (Defined before Toplevel, which embeds it.)
// Pointer focus + last position, compositor-thread-only. A motion to a new
// surface re-enters the seat pointer there; buttons/axis without a fresh
// motion reuse the last position. (Declared up here so toplevel_map can
// anchor a menu-fallback popover at the pointer; defined once, used below.)
static struct wlr_surface* g_ptr_surface = nullptr;
static double g_ptr_x = 0.0, g_ptr_y = 0.0;
// The wash window the pointer is currently over (0 if over a popup), so a
// cursor-shape change can be routed to that window's video channel (M4).
static uint32_t g_ptr_win = 0;

struct WindowSink {
    uint32_t win = 0;        // wash window id (0 until mapped)
    uint32_t video_chan = 0; // per-window video channel (0 until opened)
    uint32_t seq = 0;        // monotonic frame counter
    uint32_t sent_w = 0;     // last geometry reported to the router
    uint32_t sent_h = 0;
    SurfaceCapture cap;      // pooled BGRA capture
    SurfaceEncoder enc;      // WebP framer
    bool enc_ready = false;
    uint64_t tree_sig = ~0ull; // last captured surface-tree signature (M7);
                               // sentinel forces the first capture
    // Menu-fallback popover (DISPLAY.md §12): a parented, untitled toplevel
    // that Qt creates instead of an xdg_popup when it has no input serial
    // (programmatic menus). Streamed as a popup overlay on the parent's win
    // rather than a standalone wash window. 0/false for a normal window.
    bool popover = false;
    uint32_t popover_chan = 0;   // video-popup channel on popover_parent
    uint32_t popover_parent = 0; // parent toplevel's wash win
    int popover_off_x = 0, popover_off_y = 0; // anchor in parent canvas coords
};

// --- window-command registry + cross-thread queue ------------------
//
// Router→app commands (window.resize / window.close_requested) arrive on
// the WireConn reader thread, but wlroots is single-threaded and may only
// be touched from the compositor thread. We therefore (1) keep a registry
// mapping wash window id → its mapped surface object, and (2) marshal each
// command onto the compositor thread via a self-pipe registered in the
// wl_event_loop. The reader thread pushes a WinCmd + writes a byte; the
// event-loop fd handler drains the queue and applies the commands.
struct WinRef {
    enum Kind { XDG, X11 } kind;
    void* ptr; // Toplevel* (XDG) or XSurface* (X11)
};
static std::mutex g_reg_mu;
static std::map<uint32_t, WinRef> g_win_reg;

static void register_win(uint32_t win, WinRef::Kind kind, void* ptr) {
    if (!win) return;
    std::lock_guard<std::mutex> lk(g_reg_mu);
    g_win_reg[win] = WinRef{kind, ptr};
}
static void unregister_win(uint32_t win) {
    if (!win) return;
    std::lock_guard<std::mutex> lk(g_reg_mu);
    g_win_reg.erase(win);
}

struct WinCmd {
    std::string t;
    uint32_t win = 0, w = 0, h = 0, scale = 1;
};
static std::mutex g_cmd_mu;
static std::vector<WinCmd> g_cmds;
static int g_cmd_pipe[2] = {-1, -1};

// One mapped toplevel ↔ one wash window.
struct Toplevel {
    Server* server = nullptr;
    struct wlr_xdg_toplevel* xdg_toplevel = nullptr;
    struct wlr_scene_tree* scene_tree = nullptr;

    struct wl_listener map;
    struct wl_listener unmap;
    struct wl_listener commit;
    struct wl_listener destroy;
    // Honour client maximize/fullscreen by sizing to the virtual output, so
    // a "maximize" lands at the full screen rather than being ignored (M5).
    struct wl_listener request_maximize;
    struct wl_listener request_fullscreen;
    struct wl_listener request_move;
    struct wl_listener request_resize;

    WindowSink sink;         // shared window + capture/encode pipeline
};

// now_ms returns a monotonic millisecond timestamp for the WS frame
// header's ready_ts (latency stat only).
static uint64_t now_ms() {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

// sink_open mints the wash window for a freshly-mapped surface and opens
// its per-window video channel. The channel needs a bound shell
// (browser); without one the router replies "no shell attached" and we
// get 0 — capture still runs but frames drop until a shell binds.
static void sink_open(WindowSink& s, WireConn* conn, const std::string& title,
                      uint32_t w, uint32_t h, bool chromeless) {
    s.win = conn->create_window(title, w, h, "toplevel", 0, chromeless);
    wlr_log(WLR_INFO, "wash-display: mapped \"%s\" %ux%u -> win=%u chromeless=%d",
            title.c_str(), w, h, s.win, (int)chromeless);
    if (s.win) {
        s.video_chan = conn->open_video_channel(s.win);
        if (s.video_chan)
            wlr_log(WLR_INFO, "wash-display: win=%u video channel=%u", s.win, s.video_chan);
        else
            wlr_log(WLR_INFO, "wash-display: win=%u no video channel yet (no shell?)", s.win);
    }
}

// sink_frame captures the surface's current buffer → WebP → one framed
// message on the video channel. No-op until the window + channel exist.
// This is the capture.cpp + encode.cpp seam, shared by both paths.
static void sink_frame(WindowSink& s, WireConn* conn, struct wlr_surface* surface,
                       struct wlr_renderer* renderer,
                       int crop_x = 0, int crop_y = 0, int crop_w = 0, int crop_h = 0,
                       bool force_full = false) {
    if (!s.win || !s.video_chan) return; // not mapped / no sink
    // capture returns false when nothing changed (empty damage) — skip
    // the frame entirely, the per-frame win of damage tracking. The crop
    // (when set) is the xdg window geometry, stripping the CSD shadow margin.
    // Preserve alpha for toplevels as well as popups: transparent/shaped
    // windows should remain irregular instead of being flattened opaque.
    if (!s.cap.capture(surface, renderer, crop_x, crop_y, crop_w, crop_h,
                       force_full, true, output_scale())) return;

    // Tell the router when the content size changed so the shell frame
    // tracks it (window.geometry). Fire-and-forget; only on actual change.
    uint32_t cw = physical_to_logical(s.cap.width()), ch = physical_to_logical(s.cap.height());
    if (cw && ch && (cw != s.sent_w || ch != s.sent_h)) {
        conn->report_geometry(s.win, cw, ch);
        s.sent_w = cw;
        s.sent_h = ch;
    }

    if (!s.enc_ready || s.enc.width() != s.cap.width() ||
        s.enc.height() != s.cap.height()) {
        s.enc_ready = s.enc.init(s.cap.width(), s.cap.height());
        if (!s.enc_ready) return;
    }

    std::vector<uint8_t> frame = s.enc.encode_frame(
        s.cap.data(), s.cap.stride(), s.cap.width(), s.cap.height(),
        s.cap.dirty_x, s.cap.dirty_y, s.cap.dirty_w, s.cap.dirty_h, now_ms());
    if (frame.empty()) return;

    conn->write_channel(s.video_chan, frame.data(), frame.size());
    if ((++s.seq % 60) == 1) {
        wlr_log(WLR_INFO, "wash-display: win=%u frame seq=%u (%zu B) %dx%d dirty=%dx%d@%d,%d",
                s.win, s.seq, frame.size(), s.cap.width(), s.cap.height(),
                s.cap.dirty_w, s.cap.dirty_h, s.cap.dirty_x, s.cap.dirty_y);
    }
}

// sink_close tears down the wash window (fire-and-forget). Idempotent.
static void sink_close(WindowSink& s, WireConn* conn) {
    if (s.win) {
        conn->destroy_window(s.win);
        s.win = 0;
    }
}

// maybe_spawn_guest fork+execs $WASH_DISPLAY_EXEC (if set) as a child;
// it inherits WAYLAND_DISPLAY + XDG_RUNTIME_DIR from our env, so the
// app connects straight to this compositor. This is the DISPLAY.md §2
// "wash-display spawns the guest apps" model — discovery is free.
//
// IMPORTANT: do NOT set SIGCHLD to SIG_IGN here. wlroots manages the
// Xwayland subprocess with waitpid(), and SIG_IGN both (a) makes that
// waitpid fail with ECHILD and (b) is inherited by Xwayland, whose own
// keymap compilation forks xkbcomp and waitpid()s for it — under SIG_IGN
// that child is auto-reaped, so Xwayland reports "Failed to compile
// keymap" and aborts. We leave SIGCHLD at its default and let wlroots
// own it; the guest (a leaf process) becoming a brief zombie on exit is
// harmless for a long-lived compositor.
static void maybe_spawn_guest() {
    const char* exec = std::getenv("WASH_DISPLAY_EXEC");
    if (!exec || !*exec) return;
    pid_t pid = fork();
    if (pid == 0) {
        execl("/bin/sh", "sh", "-c", exec, (char*)nullptr);
        _exit(127);
    }
    if (pid > 0) {
        wlr_log(WLR_INFO, "wash-display: spawned guest [%s] pid=%d", exec, (int)pid);
    } else {
        wlr_log(WLR_ERROR, "wash-display: fork for guest failed");
    }
}

// --- output --------------------------------------------------------

// A tiny per-output holder so the frame callback can find its
// scene_output (kept out of Server to support multiple outputs cleanly).
struct Output {
    Server* server = nullptr;
    struct wlr_output* wlr_output = nullptr;
    struct wlr_scene_output* scene_output = nullptr;
    struct wl_listener frame;
    struct wl_listener destroy;
};

// tree_signature folds each surface's commit seq, position, texture size and
// paint order across the whole tree (root + subsurfaces), so it changes on
// repaint, moved subsurface, resize, map/unmap and stacking changes. Cheap:
// no readback.
static uint64_t sig_mix(uint64_t h, uint64_t v) {
    h ^= v + 0x9e3779b97f4a7c15ULL + (h << 6) + (h >> 2);
    return h;
}
struct SigAcc { uint64_t h = 1469598103934665603ULL; uint32_t count = 0; };
static void sig_cb(struct wlr_surface* s, int sx, int sy, void* data) {
    auto* a = static_cast<SigAcc*>(data);
    struct wlr_texture* tex = wlr_surface_get_texture(s);
    a->h = sig_mix(a->h, reinterpret_cast<uintptr_t>(s));
    a->h = sig_mix(a->h, s->current.seq);
    a->h = sig_mix(a->h, (uint32_t)sx);
    a->h = sig_mix(a->h, (uint32_t)sy);
    a->h = sig_mix(a->h, tex ? tex->width : 0);
    a->h = sig_mix(a->h, tex ? tex->height : 0);
    a->h = sig_mix(a->h, a->count);
    a->count++;
}
static uint64_t tree_signature(struct wlr_surface* root) {
    SigAcc a;
    wlr_surface_for_each_surface(root, sig_cb, &a);
    return sig_mix(a.h, a.count);
}

void output_frame(struct wl_listener* listener, void* /*data*/) {
    Output* out = wl_container_of(listener, out, frame);
    wlr_scene_output_commit(out->scene_output, nullptr);
    struct timespec now;
    clock_gettime(CLOCK_MONOTONIC, &now);
    wlr_scene_output_send_frame_done(out->scene_output, &now);

    // M7: capture each xdg window whose surface tree changed since the last
    // output frame. This is the capture driver for Wayland toplevels (the
    // commit-on-root path missed subsurface-only repaints). Snapshot the
    // registry under the lock, then capture without it (Toplevels are only
    // freed on this same compositor thread, so the pointers stay valid).
    std::vector<std::pair<uint32_t, Toplevel*>> xdgs;
    {
        std::lock_guard<std::mutex> lk(g_reg_mu);
        for (auto& [win, ref] : g_win_reg)
            if (ref.kind == WinRef::XDG && ref.ptr)
                xdgs.emplace_back(win, static_cast<Toplevel*>(ref.ptr));
    }
    for (auto& [win, t] : xdgs) {
        if (!t->xdg_toplevel) continue;
        struct wlr_surface* root = t->xdg_toplevel->base->surface;
        uint64_t sig = sig_mix(tree_signature(root), (uint64_t)output_scale());
        if (sig == t->sink.tree_sig) continue; // nothing committed → skip
        t->sink.tree_sig = sig;
        // Crop to xdg window geometry (drops the CSD shadow margin); the tree
        // composite pulls in subsurface content, and capture computes the
        // dirty rect from the per-surface seq it tracks (M7 damage).
        struct wlr_box geo{};
        wlr_xdg_surface_get_geometry(t->xdg_toplevel->base, &geo);
        sink_frame(t->sink, out->server->conn, root, out->server->renderer,
                   geo.x, geo.y, geo.width, geo.height);
    }
}

void output_destroy(struct wl_listener* listener, void* /*data*/) {
    Output* out = wl_container_of(listener, out, destroy);
    auto& outputs = out->server->outputs;
    outputs.erase(std::remove(outputs.begin(), outputs.end(), out->wlr_output), outputs.end());
    wl_list_remove(&out->frame.link);
    wl_list_remove(&out->destroy.link);
    delete out;
}

static void set_output_physical_dpi(struct wlr_output* output, int dpi) {
    if (!output) return;
    dpi = clamp_dpi(dpi);
    output->phys_width = dpi_to_mm(output_w(), dpi);
    output->phys_height = dpi_to_mm(output_h(), dpi);
}

static void configure_output_mode(struct wlr_output* output, int dpi) {
    set_output_physical_dpi(output, dpi);
    struct wlr_output_state state;
    wlr_output_state_init(&state);
    wlr_output_state_set_enabled(&state, true);
    // Headless outputs have no fixed modes; set a custom physical mode and
    // advertise the output scale separately so clients can render HiDPI
    // buffers while the shell keeps logical wash window coordinates.
    wlr_output_state_set_custom_mode(&state, output_w(), output_h(), 0);
    wlr_output_state_set_scale(&state, (float)output_scale());
    wlr_output_commit_state(output, &state);
    wlr_output_state_finish(&state);
}

static void publish_display_metrics_env(Server* server) {
    if (!server || !server->conn) return;
    server->conn->publish_env(json{
        {"WASH_DISPLAY_WIDTH", std::to_string(output_w())},
        {"WASH_DISPLAY_HEIGHT", std::to_string(output_h())},
        {"WASH_DISPLAY_SCALE", std::to_string(output_scale())},
    });
}

static void apply_display_dpi(Server* server, int dpi) {
    if (!server) return;
    dpi = clamp_dpi(dpi);
    g_output_dpi.store(dpi);
    for (auto* output : server->outputs) set_output_physical_dpi(output, dpi);
    if (server->conn) {
        server->conn->publish_env(json{{"WASH_DISPLAY_DPI", std::to_string(dpi)}});
    }
    wlr_log(WLR_INFO, "wash-display: display dpi=%d", dpi);
}

static void apply_display_metrics(Server* server, int w, int h, int scale) {
    if (!server) return;
    w = clamp_screen_w(w);
    h = clamp_screen_h(h);
    scale = clamp_scale(scale);
    g_output_w.store(w);
    g_output_h.store(h);
    g_output_scale.store(scale);
    for (auto* output : server->outputs) configure_output_mode(output, g_output_dpi.load());
    publish_display_metrics_env(server);
    wlr_log(WLR_INFO, "wash-display: display metrics=%dx%d scale=%d", w, h, scale);
}

void server_new_output(struct wl_listener* listener, void* data) {
    Server* server = wl_container_of(listener, server, new_output);
    auto* wlr_output = static_cast<struct wlr_output*>(data);

    wlr_output_init_render(wlr_output, server->allocator, server->renderer);
    server->outputs.push_back(wlr_output);
    configure_output_mode(wlr_output, g_output_dpi.load());

    auto* out = new Output();
    out->server = server;
    out->wlr_output = wlr_output;

    struct wlr_output_layout_output* l_output =
        wlr_output_layout_add_auto(server->output_layout, wlr_output);
    out->scene_output = wlr_scene_output_create(server->scene, wlr_output);
    wlr_scene_output_layout_add_output(server->scene_layout, l_output, out->scene_output);

    out->frame.notify = output_frame;
    wl_signal_add(&wlr_output->events.frame, &out->frame);
    out->destroy.notify = output_destroy;
    wl_signal_add(&wlr_output->events.destroy, &out->destroy);
}

// --- toplevel ------------------------------------------------------

// Menu-fallback popover helpers (defined after the popup-grab machinery,
// which they reuse). toplevel_setup_popover returns true when the toplevel
// is a parented, untitled menu that Qt mapped as a toplevel for lack of an
// input serial — streamed as a grabbing popup overlay instead of a window.
static bool toplevel_setup_popover(Toplevel* t);
static void toplevel_teardown_popover(Toplevel* t);
static void popover_send_frame(Toplevel* t);

void toplevel_map(struct wl_listener* listener, void* /*data*/) {
    Toplevel* t = wl_container_of(listener, t, map);

    // A Qt menu/popover opened without an input serial arrives as a parented
    // xdg_toplevel (not an xdg_popup). Render it as a popup overlay anchored
    // to its parent, not as a standalone wash window (DISPLAY.md §12).
    if (toplevel_setup_popover(t)) return;

    const char* title = t->xdg_toplevel->title;
    std::string ttl = title ? title : "Window";

    struct wlr_box geo{};
    wlr_xdg_surface_get_geometry(t->xdg_toplevel->base, &geo);
    uint32_t w = geo.width > 0 ? (uint32_t)geo.width : (uint32_t)output_logical_w();
    uint32_t h = geo.height > 0 ? (uint32_t)geo.height : (uint32_t)output_logical_h();

    // Blocking wire round-trip; safe here (compositor thread, not the
    // WireConn reader thread). Wayland xdg toplevels are chromeless: every
    // modern toolkit (GTK/Qt/Chromium/Firefox) draws its own decorations
    // (CSD), so a wash frame on top would double the titlebar (M8). The
    // guest's own button closes it; Super+drag in the shell moves it.
    sink_open(t->sink, t->server->conn, ttl, w, h, /*chromeless=*/true);
    register_win(t->sink.win, WinRef::XDG, t);
}

void toplevel_unmap(struct wl_listener* listener, void* /*data*/) {
    Toplevel* t = wl_container_of(listener, t, unmap);
    if (t->sink.popover) {
        toplevel_teardown_popover(t);
        return;
    }
    unregister_win(t->sink.win);
    sink_close(t->sink, t->server->conn);
}

void toplevel_commit(struct wl_listener* listener, void* /*data*/) {
    Toplevel* t = wl_container_of(listener, t, commit);
    // On the initial commit the client wants a configure; let it pick
    // its own size by configuring 0x0.
    if (t->xdg_toplevel->base->initial_commit) {
        wlr_xdg_toplevel_set_size(t->xdg_toplevel, 0, 0);
        return;
    }
    // A menu-fallback popover is streamed like an xdg_popup: capture+encode
    // on each commit (menus are simple surfaces with no desync subsurfaces),
    // not from output_frame (which only walks registered wash windows).
    if (t->sink.popover) {
        popover_send_frame(t);
        return;
    }
    // Capture is driven from output_frame (M7) by a surface-tree change
    // signal, not here — a root commit doesn't fire for browsers/video that
    // repaint into desynchronized subsurfaces. Nothing to do per root commit.
}

// toplevel_request_maximize / _fullscreen: ack the client's request by
// sizing it to the virtual output (M5). Without this a "maximize" is
// ignored and the window stays its old size.
void toplevel_request_maximize(struct wl_listener* listener, void* /*data*/) {
    Toplevel* t = wl_container_of(listener, t, request_maximize);
    bool on = t->xdg_toplevel->requested.maximized;
    wlr_xdg_toplevel_set_maximized(t->xdg_toplevel, on);
    wlr_xdg_toplevel_set_size(t->xdg_toplevel,
                              on ? output_logical_w() : 0,
                              on ? output_logical_h() : 0);
}

// toplevel_request_move: the guest asks for an interactive move (its CSD
// titlebar was dragged). wash owns window position and chromeless windows
// have no wash titlebar, so relay the intent to the FE element as a control
// frame on the video channel; the FE then drags the wash window following the
// pointer (M8). The grab serial is implicit — we already injected the press.
void toplevel_request_move(struct wl_listener* listener, void* /*data*/) {
    Toplevel* t = wl_container_of(listener, t, request_move);
    if (!t->sink.video_chan) return;
    std::string msg = json{{"move", true}}.dump();
    t->server->conn->write_channel(t->sink.video_chan,
                                   (const uint8_t*)msg.data(), msg.size());
    wlr_log(WLR_INFO, "wash-display: win=%u request_move -> FE", t->sink.win);
}

// toplevel_request_resize: the guest's CSD edge/corner was dragged. Like
// request_move, relay the intent (with the edge bitmask) to the FE element as
// a {resize:<edges>} control frame; the FE drives the wash window size, which
// rounds back through window.resize → set_size to repaint the guest (M8b).
void toplevel_request_resize(struct wl_listener* listener, void* data) {
    Toplevel* t = wl_container_of(listener, t, request_resize);
    auto* ev = static_cast<struct wlr_xdg_toplevel_resize_event*>(data);
    if (!t->sink.video_chan) return;
    std::string msg = json{{"resize", (int)ev->edges}}.dump();
    t->server->conn->write_channel(t->sink.video_chan,
                                   (const uint8_t*)msg.data(), msg.size());
    wlr_log(WLR_INFO, "wash-display: win=%u request_resize edges=%u -> FE",
            t->sink.win, ev->edges);
}

void toplevel_request_fullscreen(struct wl_listener* listener, void* /*data*/) {
    Toplevel* t = wl_container_of(listener, t, request_fullscreen);
    bool on = t->xdg_toplevel->requested.fullscreen;
    wlr_xdg_toplevel_set_fullscreen(t->xdg_toplevel, on);
    wlr_xdg_toplevel_set_size(t->xdg_toplevel,
                              on ? output_logical_w() : 0,
                              on ? output_logical_h() : 0);
}

void toplevel_destroy(struct wl_listener* listener, void* /*data*/) {
    Toplevel* t = wl_container_of(listener, t, destroy);
    // A popover destroyed while still mapped (no unmap first) must drop its
    // grab + registry entry so no dangling surface pointer survives.
    if (t->sink.popover) toplevel_teardown_popover(t);
    unregister_win(t->sink.win);
    sink_close(t->sink, t->server->conn);
    wl_list_remove(&t->map.link);
    wl_list_remove(&t->unmap.link);
    wl_list_remove(&t->commit.link);
    wl_list_remove(&t->destroy.link);
    wl_list_remove(&t->request_maximize.link);
    wl_list_remove(&t->request_fullscreen.link);
    wl_list_remove(&t->request_move.link);
    wl_list_remove(&t->request_resize.link);
    delete t;
}

// --- xdg-decoration: force client-side (M8) ------------------------
//
// We answer CLIENT_SIDE: Wayland toplevels are rendered chromeless (no wash
// frame), so the client must draw its OWN titlebar/buttons. This pairs with
// the chromeless window (sink_open) to give exactly ONE set of decorations.
// In practice the toolkits that matter (GTK4/libadwaita, Chromium) draw CSD
// regardless of what we answer — forcing CLIENT_SIDE just makes the apps that
// DO honour the protocol (Qt/KDE) also draw their own, instead of expecting a
// server frame we no longer draw. One Decoration per toplevel; re-forced on
// every client request_mode, self-cleaned on destroy.
struct Decoration {
    struct wlr_xdg_toplevel_decoration_v1* deco = nullptr;
    struct wl_listener request_mode;
    struct wl_listener destroy;
};

void decoration_request_mode(struct wl_listener* listener, void* /*data*/) {
    Decoration* d = wl_container_of(listener, d, request_mode);
    wlr_xdg_toplevel_decoration_v1_set_mode(
        d->deco, WLR_XDG_TOPLEVEL_DECORATION_V1_MODE_CLIENT_SIDE);
}

void decoration_destroy(struct wl_listener* listener, void* /*data*/) {
    Decoration* d = wl_container_of(listener, d, destroy);
    wl_list_remove(&d->request_mode.link);
    wl_list_remove(&d->destroy.link);
    delete d;
}

void server_new_toplevel_decoration(struct wl_listener* /*listener*/, void* data) {
    auto* deco = static_cast<struct wlr_xdg_toplevel_decoration_v1*>(data);
    auto* d = new Decoration();
    d->deco = deco;
    d->request_mode.notify = decoration_request_mode;
    wl_signal_add(&deco->events.request_mode, &d->request_mode);
    d->destroy.notify = decoration_destroy;
    wl_signal_add(&deco->events.destroy, &d->destroy);
    // Force the initial mode now (the client may not send a request).
    wlr_xdg_toplevel_decoration_v1_set_mode(
        deco, WLR_XDG_TOPLEVEL_DECORATION_V1_MODE_CLIENT_SIDE);
}

// --- xdg popups (menus/dropdowns/tooltips) → parent-window overlay -----
//
// A popup is NOT a wash window: it streams to its root toplevel's
// <wash-app-display> element as a positioned overlay canvas (so it can
// overflow the window box, like a real menu). Pixels ride a "video-popup"
// channel opened on the PARENT win; the popup's offset (relative to the
// root toplevel's geometry) rides in-band as a sub-45-byte JSON control
// frame. No wash window, no WM/router change (DISPLAY.md §12 M3).
struct Popup {
    Server* server = nullptr;
    struct wlr_xdg_popup* popup = nullptr;
    uint32_t parent_win = 0; // root toplevel's wash win
    uint32_t chan = 0;       // video-popup channel on parent_win
    int sent_x = 0, sent_y = 0;
    bool geo_sent = false;
    SurfaceCapture cap;
    SurfaceEncoder enc;
    bool enc_ready = false;
    struct wl_listener map;
    struct wl_listener unmap;
    struct wl_listener commit;
    struct wl_listener destroy;
};

// Popups by their video-popup channel id, so injected input (which has no
// wash win to key on) can reach the popup surface. Surface-based so both
// xdg_popups and X11 override-redirect menus share one registry.
// Compositor-thread only (map + inject_input both run there) → no lock.
struct PopupTarget {
    struct wlr_surface* surface = nullptr;
    Server* server = nullptr;
};
static std::map<uint32_t, PopupTarget> g_popup_reg;

// Active popup pointer-grabs (M8d). A menu (xdg_popup with a grab, or an X11
// override-redirect menu) OWNS the pointer while open: input over the PARENT
// window must reach the menu so a click-outside dismisses it and motion keeps
// the menu alive — otherwise the app sees a stray parent event and closes the
// menu instantly (the "X11 popovers don't work" symptom). A stack so nested
// submenus restore the parent menu's grab when they close. off_x/off_y is the
// popup's offset within the parent content (canvas space = sent_x/sent_y), so
// parent-window coords convert to popup-local by subtracting it.
struct PopupGrab {
    uint32_t parent_win = 0;
    struct wlr_surface* surface = nullptr;
    Server* server = nullptr;
    int off_x = 0, off_y = 0;
};
static std::vector<PopupGrab> g_popup_grabs;
static void push_popup_grab(uint32_t parent_win, struct wlr_surface* s, Server* srv,
                            int ox, int oy) {
    g_popup_grabs.push_back(PopupGrab{parent_win, s, srv, ox, oy});
}
static void pop_popup_grab(struct wlr_surface* s) {
    for (auto it = g_popup_grabs.begin(); it != g_popup_grabs.end(); ++it) {
        if (it->surface == s) { g_popup_grabs.erase(it); return; }
    }
}

// --- menu-fallback popovers (parented xdg_toplevels) -------------------
//
// Qt creates a grabbing xdg_popup for a menu ONLY when it has a fresh input
// serial. Opened without one (programmatic show, some menus), Qt instead maps
// a parented xdg_toplevel with no real window title. Other wlroots
// compositors (sway/labwc) float these as windows near the parent; wash has a
// purpose-built popup-overlay path, so we route a menu-like fallback toplevel
// through it: a video-popup channel + grab on the parent's win, exactly like
// an xdg_popup. A dialog (it sets a distinct title) stays a real window.

// toplevel_is_popover: a parented toplevel with no window title of its own
// (empty, or == app_id — Qt's menu fallback) that is not near-fullscreen.
// Conservative: anything that looks like a real/dialog window stays a window.
static bool toplevel_is_popover(struct wlr_xdg_toplevel* tl) {
    if (!tl || !tl->parent) return false;
    if (tl->requested.maximized || tl->requested.fullscreen) return false;
    const char* title = tl->title;
    const char* app = tl->app_id;
    const bool untitled = !title || !*title || (app && std::strcmp(title, app) == 0);
    if (!untitled) return false;
    // A menu/popover is small; a large parented surface is a dialog/window
    // even when it carries no title.
    struct wlr_box geo{};
    wlr_xdg_surface_get_geometry(tl->base, &geo);
    if (geo.width > output_logical_w() * 3 / 4 &&
        geo.height > output_logical_h() * 3 / 4) {
        return false;
    }
    return true;
}

static void popover_send_geometry(Toplevel* t) {
    if (!t->sink.popover_chan) return;
    json g = {{"x", t->sink.popover_off_x}, {"y", t->sink.popover_off_y}};
    std::string s = g.dump();
    t->server->conn->write_channel(t->sink.popover_chan, (const uint8_t*)s.data(), s.size());
}

static void popover_send_frame(Toplevel* t) {
    if (!t->sink.popover_chan) return;
    struct wlr_surface* surface = t->xdg_toplevel->base->surface;
    // Full surface + preserve alpha, exactly like an xdg_popup: a menu's
    // shadow/rounded corners ride the alpha, and overlay-local input maps 1:1.
    if (!t->sink.cap.capture(surface, t->server->renderer, 0, 0, 0, 0,
                             false, /*preserve_alpha=*/true, output_scale())) {
        return;
    }
    if (!t->sink.enc_ready || t->sink.enc.width() != t->sink.cap.width() ||
        t->sink.enc.height() != t->sink.cap.height()) {
        t->sink.enc_ready = t->sink.enc.init(t->sink.cap.width(), t->sink.cap.height());
        if (!t->sink.enc_ready) return;
    }
    std::vector<uint8_t> frame = t->sink.enc.encode_frame(
        t->sink.cap.data(), t->sink.cap.stride(), t->sink.cap.width(), t->sink.cap.height(),
        t->sink.cap.dirty_x, t->sink.cap.dirty_y, t->sink.cap.dirty_w, t->sink.cap.dirty_h,
        now_ms());
    if (!frame.empty())
        t->server->conn->write_channel(t->sink.popover_chan, frame.data(), frame.size());
}

static bool toplevel_setup_popover(Toplevel* t) {
    struct wlr_xdg_toplevel* tl = t->xdg_toplevel;
    if (!toplevel_is_popover(tl)) return false;
    Toplevel* pt = (tl->parent && tl->parent->base)
                       ? static_cast<Toplevel*>(tl->parent->base->data)
                       : nullptr;
    // Only chain off a real parent window; nested popover-of-popover would
    // have no parent win, so fall back to a normal window.
    if (!pt || !pt->sink.win || pt->sink.popover) return false;

    // Anchor at the last pointer position, converted from the parent's
    // surface-local space into its canvas (geometry-relative) coords — the
    // same space the FE overlay positioner and the grab path use. wlroots
    // gives a fallback menu-toplevel no positioner, so the pointer (where the
    // menu was triggered) is the best anchor.
    struct wlr_box pgeo{};
    wlr_xdg_surface_get_geometry(pt->xdg_toplevel->base, &pgeo);
    int ox = (int)g_ptr_x - pgeo.x;
    int oy = (int)g_ptr_y - pgeo.y;
    if (ox < 0) ox = 0;
    if (oy < 0) oy = 0;

    uint32_t chan = t->server->conn->open_channel_kind(pt->sink.win, "video-popup");
    if (!chan) {
        wlr_log(WLR_INFO, "wash-display: popover channel open failed (no shell?) parent=%u",
                pt->sink.win);
        return false; // no shell — let it map as a normal window instead
    }
    t->sink.popover = true;
    t->sink.popover_chan = chan;
    t->sink.popover_parent = pt->sink.win;
    t->sink.popover_off_x = ox;
    t->sink.popover_off_y = oy;

    struct wlr_surface* surf = tl->base->surface;
    popover_send_geometry(t);
    g_popup_reg[chan] = PopupTarget{surf, t->server};
    push_popup_grab(pt->sink.win, surf, t->server, ox, oy);
    popover_send_frame(t); // paint immediately; the map commit already has a buffer
    wlr_log(WLR_INFO, "wash-display: menu toplevel as popover parent_win=%u chan=%u off=%d,%d",
            pt->sink.win, chan, ox, oy);
    return true;
}

static void toplevel_teardown_popover(Toplevel* t) {
    struct wlr_surface* surf = t->xdg_toplevel->base->surface;
    pop_popup_grab(surf);
    if (t->sink.popover_chan) {
        g_popup_reg.erase(t->sink.popover_chan);
        std::string s = json{{"close", true}}.dump();
        t->server->conn->write_channel(t->sink.popover_chan, (const uint8_t*)s.data(), s.size());
        t->sink.popover_chan = 0;
    }
    t->sink.popover = false;
}

// popup_root_and_offset walks the popup parent chain to the owning
// toplevel, accumulating each popup's geometry offset, and returns that
// toplevel's wash win + the popup's offset relative to it. False if the
// chain doesn't end at a mapped toplevel (no win yet).
static bool popup_root_and_offset(struct wlr_xdg_popup* popup, uint32_t* root_win,
                                  int* ox, int* oy) {
    int x = 0, y = 0;
    struct wlr_xdg_popup* p = popup;
    for (int guard = 0; guard < 16 && p; guard++) {
        x += p->current.geometry.x;
        y += p->current.geometry.y;
        struct wlr_surface* parent = p->parent;
        if (!parent) return false;
        struct wlr_xdg_surface* pxs = wlr_xdg_surface_try_from_wlr_surface(parent);
        if (!pxs) return false;
        if (pxs->role == WLR_XDG_SURFACE_ROLE_TOPLEVEL) {
            Toplevel* t = static_cast<Toplevel*>(pxs->data);
            if (!t || !t->sink.win) return false;
            *root_win = t->sink.win;
            *ox = x;
            *oy = y;
            return true;
        }
        if (pxs->role == WLR_XDG_SURFACE_ROLE_POPUP) {
            p = pxs->popup;
            continue;
        }
        return false;
    }
    return false;
}

// popup_send_geometry writes the in-band control frame (small JSON, always
// < 45 bytes so the FE tells it apart from pixel frames).
static void popup_send_geometry(Popup* p) {
    json g = {{"x", p->sent_x}, {"y", p->sent_y}};
    std::string s = g.dump();
    p->server->conn->write_channel(p->chan, (const uint8_t*)s.data(), s.size());
}

void popup_map(struct wl_listener* listener, void* /*data*/) {
    Popup* p = wl_container_of(listener, p, map);
    uint32_t root = 0;
    int ox = 0, oy = 0;
    if (!popup_root_and_offset(p->popup, &root, &ox, &oy)) {
        wlr_log(WLR_INFO, "wash-display: popup with no mapped root toplevel — dropping");
        return;
    }
    p->parent_win = root;
    p->sent_x = ox;
    p->sent_y = oy;
    p->chan = p->server->conn->open_channel_kind(root, "video-popup");
    if (!p->chan) {
        wlr_log(WLR_INFO, "wash-display: popup channel open failed (no shell?) win=%u", root);
        return;
    }
    popup_send_geometry(p);
    p->geo_sent = true;
    g_popup_reg[p->chan] = PopupTarget{p->popup->base->surface, p->server};
    // A popup that requested a seat grab (menus, not tooltips) owns the pointer.
    if (p->popup->seat)
        push_popup_grab(root, p->popup->base->surface, p->server, ox, oy);
    wlr_log(WLR_INFO, "wash-display: popup mapped parent_win=%u chan=%u off=%d,%d grab=%d",
            root, p->chan, ox, oy, p->popup->seat ? 1 : 0);
}

void popup_unmap(struct wl_listener* listener, void* /*data*/) {
    Popup* p = wl_container_of(listener, p, unmap);
    pop_popup_grab(p->popup->base->surface);
    if (p->chan) {
        g_popup_reg.erase(p->chan);
        // Tell the FE to drop the overlay (close control frame).
        std::string s = json{{"close", true}}.dump();
        p->server->conn->write_channel(p->chan, (const uint8_t*)s.data(), s.size());
        p->chan = 0;
    }
}

void popup_commit(struct wl_listener* listener, void* /*data*/) {
    Popup* p = wl_container_of(listener, p, commit);
    if (!p->chan) return;
    // Reposition (reactive popups) → resend geometry.
    uint32_t root = 0;
    int ox = 0, oy = 0;
    if (popup_root_and_offset(p->popup, &root, &ox, &oy) &&
        (ox != p->sent_x || oy != p->sent_y)) {
        p->sent_x = ox;
        p->sent_y = oy;
        popup_send_geometry(p);
    }
    // Capture → WebP → one framed message (≥45 bytes; the FE distinguishes
    // it from the JSON control frame by length).
    struct wlr_surface* surface = p->popup->base->surface;
    if (!p->cap.capture(surface, p->server->renderer, 0, 0, 0, 0,
                        false, /*preserve_alpha=*/true, output_scale())) return;
    if (!p->enc_ready || p->enc.width() != p->cap.width() ||
        p->enc.height() != p->cap.height()) {
        p->enc_ready = p->enc.init(p->cap.width(), p->cap.height());
        if (!p->enc_ready) return;
    }
    std::vector<uint8_t> frame = p->enc.encode_frame(
        p->cap.data(), p->cap.stride(), p->cap.width(), p->cap.height(),
        p->cap.dirty_x, p->cap.dirty_y, p->cap.dirty_w, p->cap.dirty_h, now_ms());
    if (!frame.empty())
        p->server->conn->write_channel(p->chan, frame.data(), frame.size());
}

void popup_destroy(struct wl_listener* listener, void* /*data*/) {
    Popup* p = wl_container_of(listener, p, destroy);
    if (p->chan) {
        g_popup_reg.erase(p->chan);
        std::string s = json{{"close", true}}.dump();
        p->server->conn->write_channel(p->chan, (const uint8_t*)s.data(), s.size());
    }
    wl_list_remove(&p->map.link);
    wl_list_remove(&p->unmap.link);
    wl_list_remove(&p->commit.link);
    wl_list_remove(&p->destroy.link);
    delete p;
}

void new_xdg_popup(Server* server, struct wlr_xdg_popup* popup) {
    auto* p = new Popup();
    p->server = server;
    p->popup = popup;
    // Scene the popup so it gets frame-done callbacks and keeps repainting;
    // position in the scene is irrelevant (we capture the surface directly).
    wlr_scene_xdg_surface_create(&server->scene->tree, popup->base);
    p->map.notify = popup_map;
    wl_signal_add(&popup->base->surface->events.map, &p->map);
    p->unmap.notify = popup_unmap;
    wl_signal_add(&popup->base->surface->events.unmap, &p->unmap);
    p->commit.notify = popup_commit;
    wl_signal_add(&popup->base->surface->events.commit, &p->commit);
    p->destroy.notify = popup_destroy;
    wl_signal_add(&popup->base->events.destroy, &p->destroy);
}

void server_new_xdg_toplevel(struct wl_listener* listener, void* data) {
    Server* server = wl_container_of(listener, server, new_xdg_toplevel);
    // 0.17: wlr_xdg_shell has no new_toplevel signal; it emits new_surface
    // carrying a wlr_xdg_surface of any role. Take only toplevels here
    // (popups are parented by their toplevel's scene tree, not wash
    // windows); 0.18's new_toplevel did this filtering for us.
    auto* xdg_surface = static_cast<struct wlr_xdg_surface*>(data);
    // Popups (menus, dropdowns, tooltips) stream to the parent window's
    // element as an overlay rather than becoming wash windows (DISPLAY.md
    // §12 M3). 0.18's new_toplevel would have filtered these for us.
    if (xdg_surface->role == WLR_XDG_SURFACE_ROLE_POPUP) {
        new_xdg_popup(server, xdg_surface->popup);
        return;
    }
    if (xdg_surface->role != WLR_XDG_SURFACE_ROLE_TOPLEVEL) {
        return;
    }
    struct wlr_xdg_toplevel* xdg_toplevel = xdg_surface->toplevel;

    auto* t = new Toplevel();
    t->server = server;
    t->xdg_toplevel = xdg_toplevel;
    t->scene_tree =
        wlr_scene_xdg_surface_create(&server->scene->tree, xdg_toplevel->base);
    t->scene_tree->node.data = t;
    // Lets a popup resolve its root toplevel's wash win via the parent
    // surface's xdg_surface->data (popup_root_and_offset).
    xdg_toplevel->base->data = t;

    t->map.notify = toplevel_map;
    wl_signal_add(&xdg_toplevel->base->surface->events.map, &t->map);
    t->unmap.notify = toplevel_unmap;
    wl_signal_add(&xdg_toplevel->base->surface->events.unmap, &t->unmap);
    t->commit.notify = toplevel_commit;
    wl_signal_add(&xdg_toplevel->base->surface->events.commit, &t->commit);
    t->destroy.notify = toplevel_destroy;
    // 0.17: wlr_xdg_toplevel has no destroy signal; the destroy signal
    // lives on the backing wlr_xdg_surface (xdg_toplevel->base).
    wl_signal_add(&xdg_toplevel->base->events.destroy, &t->destroy);
    t->request_maximize.notify = toplevel_request_maximize;
    wl_signal_add(&xdg_toplevel->events.request_maximize, &t->request_maximize);
    t->request_fullscreen.notify = toplevel_request_fullscreen;
    wl_signal_add(&xdg_toplevel->events.request_fullscreen, &t->request_fullscreen);
    t->request_move.notify = toplevel_request_move;
    wl_signal_add(&xdg_toplevel->events.request_move, &t->request_move);
    t->request_resize.notify = toplevel_request_resize;
    wl_signal_add(&xdg_toplevel->events.request_resize, &t->request_resize);
}

#ifdef WASH_DISPLAY_XWAYLAND
// --- Xwayland (X11) surfaces (commit 10) -----------------------------
//
// X11 apps connect to the bundled Xwayland server; wlroots' embedded X
// window manager (XWM) presents each X window as a wlr_xwayland_surface.
// Unlike xdg-shell, the inner wlr_surface only becomes valid at the
// `associate` event — so the map/unmap/commit listeners are wired there
// and removed at `dissociate`. The frame path is identical to xdg: the
// shared WindowSink helpers, so an X11 window streams over the same
// capture→WebP→video-channel pipeline and inherits the wash-app-display
// decoder element via window.create.
struct XSurface {
    Server* server = nullptr;
    struct wlr_xwayland_surface* xsurf = nullptr;

    struct wl_listener associate;
    struct wl_listener dissociate;
    struct wl_listener map;
    struct wl_listener unmap;
    struct wl_listener commit;
    struct wl_listener destroy;
    struct wl_listener request_configure;
    bool surface_listeners = false; // map/unmap/commit currently wired

    WindowSink sink;

    // Override-redirect popup mode (menus/tooltips): streams to a parent
    // window's overlay over a video-popup channel instead of being a wash
    // window (DISPLAY.md §12 M3b). Mutually exclusive with sink.win.
    bool is_popup = false;
    uint32_t popup_chan = 0;
    int sent_x = 0, sent_y = 0;
};

// The most-recently-mapped normal X toplevel — the fallback parent for an
// override-redirect menu that doesn't set transient-for.
static XSurface* g_active_x_toplevel = nullptr;

static xcb_atom_t xatom(const char* name) {
    static xcb_connection_t* conn = nullptr;
    static std::map<std::string, xcb_atom_t> cache;
    auto it = cache.find(name);
    if (it != cache.end()) return it->second;
    if (!conn) {
        conn = xcb_connect(nullptr, nullptr);
        if (!conn || xcb_connection_has_error(conn)) {
            if (conn) xcb_disconnect(conn);
            conn = nullptr;
            cache[name] = static_cast<xcb_atom_t>(XCB_ATOM_NONE);
            return static_cast<xcb_atom_t>(XCB_ATOM_NONE);
        }
    }
    xcb_intern_atom_cookie_t cookie =
        xcb_intern_atom(conn, 0, (uint16_t)std::strlen(name), name);
    xcb_intern_atom_reply_t* reply = xcb_intern_atom_reply(conn, cookie, nullptr);
    xcb_atom_t atom = reply ? reply->atom : static_cast<xcb_atom_t>(XCB_ATOM_NONE);
    free(reply);
    cache[name] = atom;
    return atom;
}

static bool xsurface_has_type(struct wlr_xwayland_surface* xs, const char* type) {
    xcb_atom_t atom = xatom(type);
    if (atom == XCB_ATOM_NONE || !xs || !xs->window_type) return false;
    for (size_t i = 0; i < xs->window_type_len; i++) {
        if (xs->window_type[i] == atom) return true;
    }
    return false;
}

static bool xsurface_takes_pointer_grab(struct wlr_xwayland_surface* xs) {
    if (!xs) return false;
    // Toolkits also use override-redirect windows for passive helper UI.
    // Those surfaces must render as overlays, but must not steal all future
    // parent-window input. Menus/combo dropdowns are the pointer-owning cases.
    if (xsurface_has_type(xs, "_NET_WM_WINDOW_TYPE_TOOLTIP") ||
        xsurface_has_type(xs, "_NET_WM_WINDOW_TYPE_NOTIFICATION") ||
        xsurface_has_type(xs, "_NET_WM_WINDOW_TYPE_DND") ||
        xsurface_has_type(xs, "_NET_WM_WINDOW_TYPE_SPLASH") ||
        xsurface_has_type(xs, "_NET_WM_WINDOW_TYPE_UTILITY")) {
        return false;
    }
    if (xsurface_has_type(xs, "_NET_WM_WINDOW_TYPE_DROPDOWN_MENU") ||
        xsurface_has_type(xs, "_NET_WM_WINDOW_TYPE_POPUP_MENU") ||
        xsurface_has_type(xs, "_NET_WM_WINDOW_TYPE_COMBO") ||
        xsurface_has_type(xs, "_NET_WM_WINDOW_TYPE_MENU")) {
        return true;
    }
    // Preserve legacy behavior for untyped override-redirect popups: older
    // clients may still use them as real grabbed menus.
    return true;
}

// xpopup_send_geometry writes the offset control frame (< 45 bytes JSON).
static void xpopup_send_geometry(XSurface* x) {
    json g = {{"x", x->sent_x}, {"y", x->sent_y}};
    std::string s = g.dump();
    x->server->conn->write_channel(x->popup_chan, (const uint8_t*)s.data(), s.size());
}

// xpopup_map maps an override-redirect X window as a parent-window overlay.
// Parent = transient-for if set+mapped, else the active toplevel; offset is
// the menu's X-root position minus the parent's (X shares one root coord
// space, so this is the offset within the parent window's content frame).
static bool xpopup_map(XSurface* x) {
    XSurface* parent = nullptr;
    if (x->xsurf->parent && x->xsurf->parent->data)
        parent = static_cast<XSurface*>(x->xsurf->parent->data);
    if (!parent || parent->is_popup || !parent->sink.win)
        parent = g_active_x_toplevel;
    if (!parent || !parent->sink.win) {
        wlr_log(WLR_INFO, "wash-display: X11 override-redirect with no parent toplevel — dropping");
        return false;
    }
    uint32_t w = x->xsurf->width  > 0 ? (uint32_t)x->xsurf->width  : 1;
    uint32_t h = x->xsurf->height > 0 ? (uint32_t)x->xsurf->height : 1;
    wlr_xwayland_surface_configure(x->xsurf, x->xsurf->x, x->xsurf->y,
                                   (uint16_t)w, (uint16_t)h);
    x->popup_chan = x->server->conn->open_channel_kind(parent->sink.win, "video-popup");
    if (!x->popup_chan) {
        wlr_log(WLR_INFO, "wash-display: X11 popup channel open failed (no shell?)");
        return false;
    }
    x->sent_x = x->xsurf->x - parent->xsurf->x;
    x->sent_y = x->xsurf->y - parent->xsurf->y;
    xpopup_send_geometry(x);
    g_popup_reg[x->popup_chan] = PopupTarget{x->xsurf->surface, x->server};
    bool grab = xsurface_takes_pointer_grab(x->xsurf);
    if (grab)
        push_popup_grab(parent->sink.win, x->xsurf->surface, x->server,
                        x->sent_x, x->sent_y);
    wlr_log(WLR_INFO, "wash-display: X11 popup mapped parent_win=%u chan=%u off=%d,%d grab=%d",
            parent->sink.win, x->popup_chan, x->sent_x, x->sent_y, grab ? 1 : 0);
    return true;
}

static void xpopup_close(XSurface* x) {
    if (!x->popup_chan) return;
    pop_popup_grab(x->xsurf->surface);
    g_popup_reg.erase(x->popup_chan);
    std::string s = json{{"close", true}}.dump();
    x->server->conn->write_channel(x->popup_chan, (const uint8_t*)s.data(), s.size());
    x->popup_chan = 0;
}

void xsurface_map(struct wl_listener* listener, void* /*data*/) {
    XSurface* x = wl_container_of(listener, x, map);
    // Override-redirect = a menu/tooltip → overlay on the parent window.
    if (x->xsurf->override_redirect) {
        x->is_popup = xpopup_map(x);
        return;
    }
    const char* title = x->xsurf->title;
    std::string ttl = title ? title : "X11 Window";
    uint32_t w = x->xsurf->width  > 0 ? (uint32_t)x->xsurf->width  : (uint32_t)output_logical_w();
    uint32_t h = x->xsurf->height > 0 ? (uint32_t)x->xsurf->height : (uint32_t)output_logical_h();
    // X clients aren't sized implicitly by us — configure them to their
    // own requested geometry so they paint at the expected dimensions.
    wlr_xwayland_surface_configure(x->xsurf, x->xsurf->x, x->xsurf->y,
                                   (uint16_t)w, (uint16_t)h);
    // X11 clients (xclock, etc.) don't draw CSD — keep the wash frame so
    // they have a titlebar to move/close (M8: only Wayland is chromeless).
    sink_open(x->sink, x->server->conn, ttl, w, h, /*chromeless=*/false);
    register_win(x->sink.win, WinRef::X11, x);
    g_active_x_toplevel = x; // fallback parent for override-redirect menus
}

void xsurface_unmap(struct wl_listener* listener, void* /*data*/) {
    XSurface* x = wl_container_of(listener, x, unmap);
    if (x->is_popup) {
        xpopup_close(x);
        x->is_popup = false;
        return;
    }
    if (g_active_x_toplevel == x) g_active_x_toplevel = nullptr;
    unregister_win(x->sink.win);
    sink_close(x->sink, x->server->conn);
}

void xsurface_commit(struct wl_listener* listener, void* /*data*/) {
    XSurface* x = wl_container_of(listener, x, commit);
    if (x->is_popup) {
        if (!x->popup_chan) return;
        if (!x->sink.cap.capture(x->xsurf->surface, x->server->renderer, 0, 0, 0, 0,
                                 false, /*preserve_alpha=*/true, output_scale())) return;
        if (!x->sink.enc_ready || x->sink.enc.width() != x->sink.cap.width() ||
            x->sink.enc.height() != x->sink.cap.height()) {
            x->sink.enc_ready = x->sink.enc.init(x->sink.cap.width(), x->sink.cap.height());
            if (!x->sink.enc_ready) return;
        }
        std::vector<uint8_t> frame = x->sink.enc.encode_frame(
            x->sink.cap.data(), x->sink.cap.stride(), x->sink.cap.width(),
            x->sink.cap.height(), x->sink.cap.dirty_x, x->sink.cap.dirty_y,
            x->sink.cap.dirty_w, x->sink.cap.dirty_h, now_ms());
        if (!frame.empty())
            x->server->conn->write_channel(x->popup_chan, frame.data(), frame.size());
        return;
    }
    sink_frame(x->sink, x->server->conn, x->xsurf->surface, x->server->renderer);
}

void xsurface_associate(struct wl_listener* listener, void* /*data*/) {
    XSurface* x = wl_container_of(listener, x, associate);
    // The inner wlr_surface is valid now; add it to the scene (so it gets
    // frame callbacks and keeps rendering, e.g. a ticking clock) and wire
    // the surface-level listeners.
    wlr_scene_surface_create(&x->server->scene->tree, x->xsurf->surface);
    x->map.notify = xsurface_map;
    wl_signal_add(&x->xsurf->surface->events.map, &x->map);
    x->unmap.notify = xsurface_unmap;
    wl_signal_add(&x->xsurf->surface->events.unmap, &x->unmap);
    x->commit.notify = xsurface_commit;
    wl_signal_add(&x->xsurf->surface->events.commit, &x->commit);
    x->surface_listeners = true;
}

static void xsurface_drop_surface_listeners(XSurface* x) {
    if (x->surface_listeners) {
        wl_list_remove(&x->map.link);
        wl_list_remove(&x->unmap.link);
        wl_list_remove(&x->commit.link);
        x->surface_listeners = false;
    }
}

void xsurface_dissociate(struct wl_listener* listener, void* /*data*/) {
    XSurface* x = wl_container_of(listener, x, dissociate);
    if (x->is_popup) {
        xpopup_close(x);
        x->is_popup = false;
    } else {
        if (g_active_x_toplevel == x) g_active_x_toplevel = nullptr;
        unregister_win(x->sink.win);
        sink_close(x->sink, x->server->conn);
    }
    xsurface_drop_surface_listeners(x);
}

void xsurface_destroy(struct wl_listener* listener, void* /*data*/) {
    XSurface* x = wl_container_of(listener, x, destroy);
    if (x->is_popup) {
        xpopup_close(x);
    } else {
        if (g_active_x_toplevel == x) g_active_x_toplevel = nullptr;
        unregister_win(x->sink.win);
        sink_close(x->sink, x->server->conn);
    }
    xsurface_drop_surface_listeners(x);
    wl_list_remove(&x->associate.link);
    wl_list_remove(&x->dissociate.link);
    wl_list_remove(&x->destroy.link);
    wl_list_remove(&x->request_configure.link);
    delete x;
}

// xsurface_request_configure honours an X client resizing/repositioning
// itself. wlroots' xwm only EMITS request_configure; if no listener calls
// wlr_xwayland_surface_configure the managed (substructure-redirected) window
// never gets its ConfigureNotify and keeps its creation-time geometry — so
// xterm's Ctrl+RightClick font-size change, Java pack()/setSize(), Tk wm
// geometry, and any X dialog that autosizes after mapping all do nothing
// (REVIEW-X11-WAYLAND #2). Accepting the request is enough to propagate to the
// wash frame: the client repaints at the new size and sink_frame reports the
// changed content size (window.geometry) on the next commit.
void xsurface_request_configure(struct wl_listener* listener, void* data) {
    XSurface* x = wl_container_of(listener, x, request_configure);
    auto* ev = static_cast<struct wlr_xwayland_surface_configure_event*>(data);
    wlr_xwayland_surface_configure(x->xsurf, ev->x, ev->y, ev->width, ev->height);
}

void server_new_xwayland_surface(struct wl_listener* listener, void* data) {
    Server* server = wl_container_of(listener, server, new_xwayland_surface);
    auto* xsurf = static_cast<struct wlr_xwayland_surface*>(data);

    auto* x = new XSurface();
    x->server = server;
    x->xsurf = xsurf;
    // Lets an override-redirect menu resolve its parent toplevel's wash win
    // via xsurf->parent->data (xpopup_map).
    xsurf->data = x;

    // associate/dissociate bracket the inner wlr_surface's validity;
    // destroy is the X window going away. Override-redirect surfaces map as
    // parent-window overlays (xsurface_map branches on override_redirect).
    x->associate.notify = xsurface_associate;
    wl_signal_add(&xsurf->events.associate, &x->associate);
    x->dissociate.notify = xsurface_dissociate;
    wl_signal_add(&xsurf->events.dissociate, &x->dissociate);
    x->destroy.notify = xsurface_destroy;
    wl_signal_add(&xsurf->events.destroy, &x->destroy);
    x->request_configure.notify = xsurface_request_configure;
    wl_signal_add(&xsurf->events.request_configure, &x->request_configure);
}
#endif // WASH_DISPLAY_XWAYLAND

// --- input injection (DISPLAY.md §6) -------------------------------
//
// The FE <wash-app-display> element batches pointer/keyboard/scroll events
// into one app_msg {kind:"input", win, events:[...]} per rAF. main.cpp's
// app_msg handler (reader thread) pushes each payload onto g_inputs and
// wakes the self-pipe; on_cmd_pipe drains + injects them HERE on the
// compositor thread, where wlroots is safe to touch.
static std::mutex g_input_mu;
static std::vector<json> g_inputs;


// winref_surface / winref_server pull the inner wlr_surface and owning
// Server out of a registry entry. The surface may be null (X11 surface not
// yet associated). Caller holds no lock; the WinRef was already copied out
// of g_win_reg under g_reg_mu.
static struct wlr_surface* winref_surface(const WinRef& ref) {
    if (ref.kind == WinRef::XDG) {
        Toplevel* t = static_cast<Toplevel*>(ref.ptr);
        return t->xdg_toplevel->base->surface;
    }
#ifdef WASH_DISPLAY_XWAYLAND
    XSurface* x = static_cast<XSurface*>(ref.ptr);
    return x->xsurf ? x->xsurf->surface : nullptr;
#else
    return nullptr;
#endif
}
static Server* winref_server(const WinRef& ref) {
    if (ref.kind == WinRef::XDG) return static_cast<Toplevel*>(ref.ptr)->server;
#ifdef WASH_DISPLAY_XWAYLAND
    return static_cast<XSurface*>(ref.ptr)->server;
#else
    return nullptr;
#endif
}

// resolve_win looks up a wash window id and returns its surface + server.
static bool resolve_win(uint32_t win, struct wlr_surface** surf, Server** srv) {
    WinRef ref;
    {
        std::lock_guard<std::mutex> lk(g_reg_mu);
        auto it = g_win_reg.find(win);
        if (it == g_win_reg.end()) return false;
        ref = it->second;
    }
    *surf = winref_surface(ref);
    *srv = winref_server(ref);
    return *surf != nullptr && *srv != nullptr;
}

// win_video_chan returns a window's per-window video channel (0 if unknown),
// used to route cursor-shape control frames to the right element.
static uint32_t win_video_chan(uint32_t win) {
    std::lock_guard<std::mutex> lk(g_reg_mu);
    auto it = g_win_reg.find(win);
    if (it == g_win_reg.end()) return 0;
    if (it->second.kind == WinRef::XDG)
        return static_cast<Toplevel*>(it->second.ptr)->sink.video_chan;
#ifdef WASH_DISPLAY_XWAYLAND
    return static_cast<XSurface*>(it->second.ptr)->sink.video_chan;
#else
    return 0;
#endif
}

// code_to_keycode maps a DOM KeyboardEvent.code to a Linux evdev keycode
// (KEY_* from <linux/input-event-codes.h>). The xkb keymap turns the evdev
// code (+8 internally) into the keysym, so this table is layout-agnostic —
// only physical-key identity matters here. Returns 0 for unmapped codes.
static uint32_t code_to_keycode(const std::string& c) {
    static const std::unordered_map<std::string, uint32_t> m = {
        {"KeyA", KEY_A}, {"KeyB", KEY_B}, {"KeyC", KEY_C}, {"KeyD", KEY_D},
        {"KeyE", KEY_E}, {"KeyF", KEY_F}, {"KeyG", KEY_G}, {"KeyH", KEY_H},
        {"KeyI", KEY_I}, {"KeyJ", KEY_J}, {"KeyK", KEY_K}, {"KeyL", KEY_L},
        {"KeyM", KEY_M}, {"KeyN", KEY_N}, {"KeyO", KEY_O}, {"KeyP", KEY_P},
        {"KeyQ", KEY_Q}, {"KeyR", KEY_R}, {"KeyS", KEY_S}, {"KeyT", KEY_T},
        {"KeyU", KEY_U}, {"KeyV", KEY_V}, {"KeyW", KEY_W}, {"KeyX", KEY_X},
        {"KeyY", KEY_Y}, {"KeyZ", KEY_Z},
        {"Digit1", KEY_1}, {"Digit2", KEY_2}, {"Digit3", KEY_3},
        {"Digit4", KEY_4}, {"Digit5", KEY_5}, {"Digit6", KEY_6},
        {"Digit7", KEY_7}, {"Digit8", KEY_8}, {"Digit9", KEY_9},
        {"Digit0", KEY_0},
        {"Enter", KEY_ENTER}, {"Escape", KEY_ESC}, {"Backspace", KEY_BACKSPACE},
        {"Tab", KEY_TAB}, {"Space", KEY_SPACE}, {"Minus", KEY_MINUS},
        {"Equal", KEY_EQUAL}, {"BracketLeft", KEY_LEFTBRACE},
        {"BracketRight", KEY_RIGHTBRACE}, {"Backslash", KEY_BACKSLASH},
        {"Semicolon", KEY_SEMICOLON}, {"Quote", KEY_APOSTROPHE},
        {"Backquote", KEY_GRAVE}, {"Comma", KEY_COMMA}, {"Period", KEY_DOT},
        {"Slash", KEY_SLASH}, {"CapsLock", KEY_CAPSLOCK},
        {"ShiftLeft", KEY_LEFTSHIFT}, {"ShiftRight", KEY_RIGHTSHIFT},
        {"ControlLeft", KEY_LEFTCTRL}, {"ControlRight", KEY_RIGHTCTRL},
        {"AltLeft", KEY_LEFTALT}, {"AltRight", KEY_RIGHTALT},
        {"MetaLeft", KEY_LEFTMETA}, {"MetaRight", KEY_RIGHTMETA},
        {"ArrowUp", KEY_UP}, {"ArrowDown", KEY_DOWN},
        {"ArrowLeft", KEY_LEFT}, {"ArrowRight", KEY_RIGHT},
        {"Home", KEY_HOME}, {"End", KEY_END}, {"PageUp", KEY_PAGEUP},
        {"PageDown", KEY_PAGEDOWN}, {"Insert", KEY_INSERT}, {"Delete", KEY_DELETE},
        {"F1", KEY_F1}, {"F2", KEY_F2}, {"F3", KEY_F3}, {"F4", KEY_F4},
        {"F5", KEY_F5}, {"F6", KEY_F6}, {"F7", KEY_F7}, {"F8", KEY_F8},
        {"F9", KEY_F9}, {"F10", KEY_F10}, {"F11", KEY_F11}, {"F12", KEY_F12},
    };
    auto it = m.find(c);
    return it == m.end() ? 0 : it->second;
}

// inject_input applies one FE input payload to its target surface. Routes
// by the payload's "win" (NOT the app_msg envelope win — cross-instance
// app_msgs arrive on the instance's primary window). Compositor thread only.
static void inject_input(const json& data) {
    struct wlr_surface* surface = nullptr;
    Server* srv = nullptr;
    // coord_d{x,y} is added to the FE's payload coords to map them into the
    // target surface's local space (see each branch).
    int coord_dx = 0, coord_dy = 0;
    if (uint32_t pc = data.value("popup_chan", 0U); pc) {
        // Pointer is over the popup overlay → straight to the popup surface;
        // the overlay's coords are already popup-local.
        auto it = g_popup_reg.find(pc);
        if (it == g_popup_reg.end()) return;
        surface = it->second.surface;
        srv = it->second.server;
        g_ptr_win = 0; // over a popup → cursor-shape routing skips it (v1)
    } else {
        uint32_t win = data.value("win", 0U);
        PopupGrab* active_grab = nullptr;
        for (auto it = g_popup_grabs.rbegin(); it != g_popup_grabs.rend(); ++it) {
            if (it->parent_win == win) {
                active_grab = &*it;
                break;
            }
        }
        if (active_grab) {
            // A menu holds a pointer grab (M8d): parent-window input is
            // redirected to it. Parent-canvas coords → popup-local by
            // subtracting the popup's offset within the parent. A press that
            // lands outside the popup bounds reaches the menu client as an
            // outside-click → it dismisses itself.
            surface = active_grab->surface;
            srv = active_grab->server;
            g_ptr_win = 0;
            coord_dx = -active_grab->off_x;
            coord_dy = -active_grab->off_y;
        } else {
            if (!resolve_win(win, &surface, &srv)) return;
            g_ptr_win = win;
            // The capture is cropped to the xdg window geometry (M5c — drops the
            // CSD shadow margin), so the FE's canvas coords are relative to the
            // crop origin. Add it back for true surface-local coords (X11 has no
            // xdg geometry → zero).
            if (struct wlr_xdg_surface* xs = wlr_xdg_surface_try_from_wlr_surface(surface)) {
                struct wlr_box geo{};
                wlr_xdg_surface_get_geometry(xs, &geo);
                coord_dx = geo.x;
                coord_dy = geo.y;
            }
        }
    }
    if (!surface || !srv || !srv->seat) return;
    if (!data.contains("events") || !data["events"].is_array()) return;

    uint32_t log_tgt = data.value("popup_chan", 0U) ? data.value("popup_chan", 0U)
                                                    : data.value("win", 0U);
    struct wlr_seat* seat = srv->seat;
    uint32_t t = (uint32_t)now_ms();
    bool ptr_touched = false;

    // Pointer focus follows the actual (sub)surface under the cursor, not just
    // the root toplevel. Clients like Chromium/Electron render dropdowns,
    // <select> popups and other interactive content into wl_subsurfaces; we
    // must descend with wlr_surface_surface_at and enter THAT child with its
    // own surface-local coords. Entering only the root surface lands the click
    // on the wrong surface and the child widget never sees it — the omnibox
    // "click does nothing, keyboard works" bug. g_ptr_x/g_ptr_y stay in the
    // resolved-target's local space (root toplevel / popup); cx/cy carry the
    // descended child-local coords for the current event.
    double cx = 0, cy = 0;
    auto focus_child = [&](double rx, double ry) -> struct wlr_surface* {
        double sx = 0, sy = 0;
        struct wlr_surface* child = wlr_surface_surface_at(surface, rx, ry, &sx, &sy);
        if (!child) { child = surface; sx = rx; sy = ry; } // over a margin / no input region
        if (g_ptr_surface != child) {
            wlr_seat_pointer_notify_enter(seat, child, sx, sy);
            g_ptr_surface = child;
        }
        cx = sx; cy = sy;
        return child;
    };

    for (const auto& e : data["events"]) {
        const std::string ev = e.value("ev", std::string());
        if (ev == "motion") {
            double x = e.value("x", 0.0) + coord_dx, y = e.value("y", 0.0) + coord_dy;
            g_ptr_x = x;
            g_ptr_y = y;
            focus_child(x, y);
            wlr_seat_pointer_notify_motion(seat, t, cx, cy);
            ptr_touched = true;
        } else if (ev == "button") {
            const std::string btn = e.value("btn", std::string("left"));
            uint32_t code = btn == "right"  ? BTN_RIGHT
                          : btn == "middle" ? BTN_MIDDLE
                                            : BTN_LEFT;
            bool down = e.value("state", std::string()) == "down";
            struct wlr_surface* child = focus_child(g_ptr_x, g_ptr_y);
            wlr_seat_pointer_notify_button(
                seat, t, code, down ? WLR_BUTTON_PRESSED : WLR_BUTTON_RELEASED);
            wlr_log(WLR_INFO, "wash-display: inject win=%u button %s %s @ %.0f,%.0f%s",
                    log_tgt, btn.c_str(), down ? "down" : "up", cx, cy,
                    child != surface ? " [subsurface]" : "");
            ptr_touched = true;
        } else if (ev == "axis") {
            focus_child(g_ptr_x, g_ptr_y);
            const std::string ax = e.value("axis", std::string("v"));
            double delta = e.value("delta", 0.0);
            enum wlr_axis_orientation orient =
                ax == "h" ? WLR_AXIS_ORIENTATION_HORIZONTAL
                          : WLR_AXIS_ORIENTATION_VERTICAL;
            // The FE forwards a 120-per-notch high-resolution delta (the
            // wheel convention). wlroots wants a continuous `value` plus the
            // discrete hi-res step; ~15 units per notch matches a real wheel.
            double value = delta / 120.0 * 15.0;
            wlr_seat_pointer_notify_axis(seat, t, orient, value, (int32_t)delta,
                                         WLR_AXIS_SOURCE_WHEEL);
            ptr_touched = true;
        } else if (ev == "key") {
            uint32_t kc = code_to_keycode(e.value("code", std::string()));
            if (kc && srv->vkbd_inited) {
                // Drive the virtual keyboard: this updates xkb state and
                // fires vkbd.events.key/modifiers, which forward to the seat
                // (so the client gets the key WITH modifier state). Focus
                // must already be set via window.focus.
                struct wlr_keyboard_key_event ke{};
                ke.time_msec = t;
                ke.keycode = kc;
                ke.update_state = true;
                ke.state = e.value("state", std::string()) == "down"
                               ? WL_KEYBOARD_KEY_STATE_PRESSED
                               : WL_KEYBOARD_KEY_STATE_RELEASED;
                wlr_keyboard_notify_key(&srv->vkbd, &ke);
                wlr_log(WLR_INFO, "wash-display: inject win=%u key keycode=%u %s", log_tgt,
                        kc, ke.state == WL_KEYBOARD_KEY_STATE_PRESSED ? "down" : "up");
            }
        }
        // "motion_rel" (pointer-lock / relative) is deferred — needs
        // wlr_relative_pointer + a locked-pointer constraint.
    }
    if (ptr_touched) wlr_seat_pointer_notify_frame(seat);
}

// --- virtual-keyboard → seat forwarding ----------------------------
void vkbd_handle_key(struct wl_listener* listener, void* data) {
    Server* s = wl_container_of(listener, s, vkbd_key);
    auto* ev = static_cast<struct wlr_keyboard_key_event*>(data);
    wlr_seat_set_keyboard(s->seat, &s->vkbd);
    wlr_seat_keyboard_notify_key(s->seat, ev->time_msec, ev->keycode, ev->state);
}
void vkbd_handle_modifiers(struct wl_listener* listener, void* /*data*/) {
    Server* s = wl_container_of(listener, s, vkbd_modifiers);
    wlr_seat_set_keyboard(s->seat, &s->vkbd);
    wlr_seat_keyboard_notify_modifiers(s->seat, &s->vkbd.modifiers);
}

// --- cursor shape (cursor-shape-v1) → FE CSS cursor (M4) ------------
//
// A client names its cursor (text/pointer/ew-resize/…); we forward the
// name on the focused window's video channel as a sub-45-byte JSON control
// frame ({cursor:"<name>"}), and the element sets it as the CSS cursor. The
// cursor-shape names are the CSS cursor keywords, so the mapping is identity.
void handle_cursor_shape(struct wl_listener* listener, void* data) {
    Server* s = wl_container_of(listener, s, cursor_shape_request);
    auto* ev = static_cast<struct wlr_cursor_shape_manager_v1_request_set_shape_event*>(data);
    if (ev->device_type != WLR_CURSOR_SHAPE_MANAGER_V1_DEVICE_TYPE_POINTER) return;
    if (!g_ptr_win) return; // pointer over a popup (v1: skip) or nothing
    uint32_t chan = win_video_chan(g_ptr_win);
    if (!chan) return;
    const char* name = wlr_cursor_shape_v1_name(ev->shape);
    std::string msg = json{{"cursor", name ? name : "default"}}.dump();
    s->conn->write_channel(chan, (const uint8_t*)msg.data(), msg.size());
    wlr_log(WLR_INFO, "wash-display: cursor win=%u shape=%s", g_ptr_win,
            name ? name : "default");
}

// --- clipboard bridge (DISPLAY.md §7) ------------------------------
//
// wash's clipboard is eager + router-held; Wayland/X11 selections are lazy
// + owner-served. We bridge both directions reusing clipboard.set/get:
//   guest→wash: a guest takes the selection → we read its bytes → clipboard.set
//   wash→guest: clipboard.changed → install a data source that serves
//               wash's bytes on demand (lazy) to whoever pastes.
// X11 is automatic: wlroots' xwm bridges X11 CLIPBOARD ↔ the seat selection
// once the seat is set, so handling the Wayland seat selection covers both.

static Server* g_server = nullptr;          // set in run_compositor
static std::mutex g_clip_mu;
static std::vector<std::string> g_clip_offers; // pending wash→guest mimes

// pick_mime returns the best-matching offered mime from a source, or null.
static const char* pick_mime(struct wlr_data_source* src) {
    const char* text = nullptr;
    const char* image = nullptr;
    // Iterate the wl_array by hand: the wl_array_for_each macro assigns
    // void*->char** unchecked, which is an error under C++.
    char** items = static_cast<char**>(src->mime_types.data);
    size_t count = src->mime_types.size / sizeof(char*);
    for (size_t i = 0; i < count; i++) {
        const char* mt = items[i];
        if (!mt) continue;
        if (std::strcmp(mt, "text/plain;charset=utf-8") == 0) return mt; // best
        if (!text && (std::strcmp(mt, "text/plain") == 0 ||
                      std::strcmp(mt, "UTF8_STRING") == 0))
            text = mt;
        if (!image && std::strcmp(mt, "image/png") == 0) image = mt;
    }
    return text ? text : image;
}

// guest→wash: a guest (Wayland client, or X11 app via xwm) took ownership of
// the selection. Read it through a pipe and store it in wash's clipboard.
void handle_set_selection(struct wl_listener* listener, void* /*data*/) {
    Server* s = wl_container_of(listener, s, set_selection);
    struct wlr_data_source* src = s->seat->selection_source;
    if (!src || src == s->wash_source) return; // empty, or our own → no loop
    const char* chosen = pick_mime(src);
    if (!chosen) return;
    std::string washmime = std::strncmp(chosen, "image/", 6) == 0 ? chosen : "text/plain";
    wlr_log(WLR_INFO, "wash-display: clipboard guest->wash mime=%s", chosen);

    int fds[2];
    if (pipe(fds) != 0) return;
    // Hand the write end to the owner; it fills it asynchronously.
    wlr_data_source_send(src, chosen, fds[1]);
    close(fds[1]);
    int rfd = fds[0];
    WireConn* conn = s->conn;
    std::thread([conn, washmime, rfd] {
        std::vector<uint8_t> buf;
        char tmp[4096];
        ssize_t n;
        while ((n = read(rfd, tmp, sizeof tmp)) > 0)
            buf.insert(buf.end(), tmp, tmp + n);
        close(rfd);
        if (conn && !buf.empty()) conn->clipboard_set(washmime, buf);
    }).detach();
}

// request_set_selection: a Wayland client asks to own the selection. Accept
// it (X11 owners go straight through wlr_seat_set_selection via xwm, so they
// don't pass here — but both end up firing set_selection above).
void handle_request_set_selection(struct wl_listener* listener, void* data) {
    Server* s = wl_container_of(listener, s, req_set_selection);
    auto* ev = static_cast<struct wlr_seat_request_set_selection_event*>(data);
    wlr_seat_set_selection(s->seat, ev->source, ev->serial);
}

// wash→guest: a data source backed by wash's clipboard. Its bytes are pulled
// lazily (clipboard.get) only when a guest actually pastes.
struct WashClipSource {
    struct wlr_data_source base; // MUST be first (cast target)
    Server* server = nullptr;
};

void wash_src_send(struct wlr_data_source* source, const char* /*mime*/, int32_t fd) {
    auto* w = reinterpret_cast<WashClipSource*>(source);
    WireConn* conn = w->server ? w->server->conn : nullptr;
    // Pull + write off-thread: clipboard.get blocks on the router reply and
    // the write can block on a slow consumer; neither should stall the
    // compositor event loop.
    std::thread([conn, fd] {
        std::string mime;
        std::vector<uint8_t> bytes;
        if (conn && conn->clipboard_get(mime, bytes)) {
            const uint8_t* p = bytes.data();
            size_t left = bytes.size();
            while (left) {
                ssize_t n = write(fd, p, left);
                if (n <= 0) break;
                p += n;
                left -= (size_t)n;
            }
        }
        close(fd);
    }).detach();
}

void wash_src_destroy(struct wlr_data_source* source) {
    auto* w = reinterpret_cast<WashClipSource*>(source);
    if (w->server && w->server->wash_source == source) w->server->wash_source = nullptr;
    delete w;
}

static const struct wlr_data_source_impl wash_src_impl = {
    /*send*/ wash_src_send, /*accept*/ nullptr, /*destroy*/ wash_src_destroy,
    /*dnd_drop*/ nullptr, /*dnd_finish*/ nullptr, /*dnd_action*/ nullptr};

// install_wash_source claims the seat selection with a source that serves
// wash's clipboard, advertising a small mime set so common guests match.
static void install_wash_source(Server* srv, const std::string& mime) {
    if (!srv || !srv->seat) return;
    auto* w = new WashClipSource();
    wlr_data_source_init(&w->base, &wash_src_impl);
    w->server = srv;
    auto add = [&](const char* mt) {
        char** p = static_cast<char**>(wl_array_add(&w->base.mime_types, sizeof(char*)));
        if (p) *p = strdup(mt);
    };
    if (mime.rfind("image/", 0) == 0) {
        add(mime.c_str());
    } else {
        add("text/plain;charset=utf-8");
        add("text/plain");
        add("UTF8_STRING");
        add("STRING");
        add("TEXT");
    }
    srv->wash_source = &w->base;
    wlr_seat_set_selection(srv->seat, &w->base, wl_display_next_serial(srv->display));
    wlr_log(WLR_INFO, "wash-display: clipboard wash->guest offered mime=%s", mime.c_str());
}

// post_clip_offer marshals a clipboard.changed (reader thread) onto the
// compositor thread, where the seat selection may be touched.
static void post_clip_offer(const std::string& mime) {
    {
        std::lock_guard<std::mutex> lk(g_clip_mu);
        g_clip_offers.push_back(mime);
    }
    if (g_cmd_pipe[1] >= 0) {
        char b = 1;
        ssize_t n = write(g_cmd_pipe[1], &b, 1);
        (void)n;
    }
}

// --- apply window commands on the compositor thread ----------------

// apply_win_cmd resolves a wash window id to its surface and applies a
// resize/close/focus. Runs on the compositor thread (from the event-loop
// pipe handler), so wlroots calls are safe. Unknown win (already gone) is a
// no-op.
static void apply_win_cmd(const WinCmd& c) {
    if (c.t == "display.dpi") {
        apply_display_dpi(g_server, (int)c.w);
        return;
    }
    if (c.t == "display.metrics") {
        apply_display_metrics(g_server, (int)c.w, (int)c.h, (int)c.scale);
        return;
    }
    wlr_log(WLR_INFO, "wash-display: apply win cmd t=%s win=%u %ux%u",
            c.t.c_str(), c.win, c.w, c.h);
    WinRef ref;
    {
        std::lock_guard<std::mutex> lk(g_reg_mu);
        auto it = g_win_reg.find(c.win);
        if (it == g_win_reg.end()) {
            wlr_log(WLR_ERROR, "wash-display: win cmd t=%s for unknown win=%u (gone?)",
                    c.t.c_str(), c.win);
            return;
        }
        ref = it->second;
    }
    // Focus is generic across surface kinds: set/clear the seat's keyboard
    // focus so injected keys land in (only) the focused window's surface.
    // Router-authoritative (DISPLAY.md §6) — the WM decides who has focus.
    if (c.t == "window.focus" || c.t == "window.unfocus") {
        Server* srv = winref_server(ref);
        struct wlr_surface* surface = winref_surface(ref);
        const bool active = c.t == "window.focus";
        if (ref.kind == WinRef::XDG) {
            auto* t = static_cast<Toplevel*>(ref.ptr);
            if (t && t->xdg_toplevel) {
                wlr_xdg_toplevel_set_activated(t->xdg_toplevel, active);
            }
        }
#ifdef WASH_DISPLAY_XWAYLAND
        else {
            auto* x = static_cast<XSurface*>(ref.ptr);
            if (x && x->xsurf) {
                wlr_xwayland_surface_activate(x->xsurf, active);
            }
        }
#endif
        if (srv && srv->seat) {
            if (active && surface) {
                struct wlr_keyboard* kb = wlr_seat_get_keyboard(srv->seat);
                wlr_seat_keyboard_notify_enter(srv->seat, surface,
                                               kb ? kb->keycodes : nullptr,
                                               kb ? kb->num_keycodes : 0,
                                               kb ? &kb->modifiers : nullptr);
            } else {
                wlr_seat_keyboard_notify_clear_focus(srv->seat);
            }
        }
        return;
    }
    if (ref.kind == WinRef::XDG) {
        Toplevel* t = static_cast<Toplevel*>(ref.ptr);
        if (c.t == "window.resize" && c.w && c.h) {
            wlr_log(WLR_INFO, "wash-display: xdg set_size win=%u -> %ux%u", c.win, c.w, c.h);
            wlr_xdg_toplevel_set_size(t->xdg_toplevel, c.w, c.h);
        } else if (c.t == "window.close_requested") {
            // Veto the router's auto-destroy; ask the client to close
            // politely. Real teardown happens via destroy_window when the
            // client actually exits (toplevel_destroy).
            t->server->conn->confirm_close(c.win, false);
            wlr_xdg_toplevel_send_close(t->xdg_toplevel);
        }
    } else {
#ifdef WASH_DISPLAY_XWAYLAND
        XSurface* x = static_cast<XSurface*>(ref.ptr);
        if (c.t == "window.resize" && c.w && c.h) {
            wlr_log(WLR_INFO, "wash-display: X11 configure win=%u -> %ux%u", c.win, c.w, c.h);
            wlr_xwayland_surface_configure(x->xsurf, x->xsurf->x, x->xsurf->y,
                                           (uint16_t)c.w, (uint16_t)c.h);
        } else if (c.t == "window.close_requested") {
            x->server->conn->confirm_close(c.win, false);
            wlr_xwayland_surface_close(x->xsurf);
        }
#endif
    }
}

// on_cmd_pipe drains the cross-thread command queue when the reader
// thread signals via the self-pipe. wl_event_loop_fd_func_t signature.
int on_cmd_pipe(int fd, uint32_t /*mask*/, void* /*data*/) {
    char buf[64];
    while (read(fd, buf, sizeof buf) > 0) { /* drain wakeups */ }
    std::vector<WinCmd> cmds;
    {
        std::lock_guard<std::mutex> lk(g_cmd_mu);
        cmds.swap(g_cmds);
    }
    for (const auto& c : cmds) apply_win_cmd(c);

    // Input events ride the SAME wakeup byte (separate queue, higher volume).
    std::vector<json> inputs;
    {
        std::lock_guard<std::mutex> lk(g_input_mu);
        inputs.swap(g_inputs);
    }
    for (const auto& d : inputs) inject_input(d);

    // wash→guest clipboard offers (claim the seat selection).
    std::vector<std::string> offers;
    {
        std::lock_guard<std::mutex> lk(g_clip_mu);
        offers.swap(g_clip_offers);
    }
    for (const auto& mime : offers) install_wash_source(g_server, mime);
    return 0;
}

} // namespace

void post_input(const json& data) {
    {
        std::lock_guard<std::mutex> lk(g_input_mu);
        g_inputs.push_back(data);
    }
    // Wake the compositor thread via the same self-pipe the window commands
    // use. Byte value is irrelevant — on_cmd_pipe drains both queues.
    if (g_cmd_pipe[1] >= 0) {
        char b = 1;
        ssize_t n = write(g_cmd_pipe[1], &b, 1);
        (void)n;
    }
}

void post_display_dpi(int dpi) {
    dpi = clamp_dpi(dpi);
    g_output_dpi.store(dpi);
    {
        std::lock_guard<std::mutex> lk(g_cmd_mu);
        g_cmds.push_back(WinCmd{"display.dpi", 0, (uint32_t)dpi, 0});
    }
    if (g_cmd_pipe[1] >= 0) {
        char b = 1;
        ssize_t n = write(g_cmd_pipe[1], &b, 1);
        (void)n;
    }
}

void post_display_metrics(int width, int height, int scale) {
    width = clamp_screen_w(width);
    height = clamp_screen_h(height);
    scale = clamp_scale(scale);
    g_output_w.store(width);
    g_output_h.store(height);
    g_output_scale.store(scale);
    {
        std::lock_guard<std::mutex> lk(g_cmd_mu);
        WinCmd c;
        c.t = "display.metrics";
        c.w = (uint32_t)width;
        c.h = (uint32_t)height;
        c.scale = (uint32_t)scale;
        g_cmds.push_back(c);
    }
    if (g_cmd_pipe[1] >= 0) {
        char b = 1;
        ssize_t n = write(g_cmd_pipe[1], &b, 1);
        (void)n;
    }
}

int run_compositor(WireConn& conn) {
    wlr_log_init(WLR_INFO, nullptr);

    Server server{};
    server.conn = &conn;

    server.display = wl_display_create();
    if (!server.display) {
        std::fprintf(stderr, "wash-display: wl_display_create failed\n");
        return 1;
    }

    server.backend = wlr_headless_backend_create(server.display);
    if (!server.backend) {
        std::fprintf(stderr, "wash-display: headless backend create failed\n");
        return 1;
    }

    server.renderer = wlr_renderer_autocreate(server.backend);
    if (!server.renderer) {
        std::fprintf(stderr, "wash-display: renderer autocreate failed\n");
        return 1;
    }
    wlr_renderer_init_wl_display(server.renderer, server.display);

    server.allocator = wlr_allocator_autocreate(server.backend, server.renderer);
    if (!server.allocator) {
        std::fprintf(stderr, "wash-display: allocator autocreate failed\n");
        return 1;
    }
    g_capture_allocator = server.allocator; // capture.cpp mints render targets here

    struct wlr_compositor* compositor =
        wlr_compositor_create(server.display, 5, server.renderer);
    wlr_subcompositor_create(server.display);
    wlr_data_device_manager_create(server.display);

    server.output_layout = wlr_output_layout_create();
    // Disable wlroots scene occlusion culling BEFORE creating the scene (the
    // flag is read once in wlr_scene_create). wash is NOT a stacked desktop:
    // every window is captured per-surface and composited INDEPENDENTLY in the
    // browser, all simultaneously visible. But all surfaces scene at the output
    // origin, so wlroots' culling gives a window covered by a newer one an
    // empty visible region → wlr_scene_output_send_frame_done never fires for
    // it → the covered-but-visible window freezes (Chromium's rAF stalls, GTK
    // stops repainting) even as the user types into it (REVIEW-X11-WAYLAND #1).
    // Spreading windows apart in scene space can't fix it here — the virtual
    // output is a single normal-sized screen, so a spread-out window falls
    // outside the output and loses frame-done just the same. Disabling culling
    // is one of the review's sanctioned fixes and is near-free: the composited
    // scene output isn't what wash ships (capture reads surfaces directly), so
    // "render everything, cull nothing" costs nothing and unfreezes every
    // occluded window.
    setenv("WLR_SCENE_DISABLE_VISIBILITY", "1", /*overwrite=*/1);
    server.scene = wlr_scene_create();
    server.scene_layout =
        wlr_scene_attach_output_layout(server.scene, server.output_layout);

    // xdg-output: clients querying logical output geometry (screen size for
    // maximize/centering) get the real virtual-output dimensions (M5).
    wlr_xdg_output_manager_v1_create(server.display, server.output_layout);

    server.new_output.notify = server_new_output;
    wl_signal_add(&server.backend->events.new_output, &server.new_output);

    server.xdg_shell = wlr_xdg_shell_create(server.display, 3);
    server.new_xdg_toplevel.notify = server_new_xdg_toplevel;
    // 0.17: subscribe to new_surface (no new_toplevel signal); the handler
    // filters for the toplevel role.
    wl_signal_add(&server.xdg_shell->events.new_surface, &server.new_xdg_toplevel);

    // Cross-thread window-command path: the WireConn reader thread pushes
    // resize/close commands onto g_cmds + writes a byte to this pipe; the
    // event-loop handler drains them on THIS (compositor) thread, where
    // wlroots calls are safe. Set the handler only after the pipe exists.
    if (pipe2(g_cmd_pipe, O_NONBLOCK | O_CLOEXEC) == 0) {
        struct wl_event_loop* loop = wl_display_get_event_loop(server.display);
        wl_event_loop_add_fd(loop, g_cmd_pipe[0], WL_EVENT_READABLE,
                             on_cmd_pipe, nullptr);
        conn.on_window_cmd([](const std::string& t, uint32_t win,
                              uint32_t w, uint32_t h) {
            {
                std::lock_guard<std::mutex> lk(g_cmd_mu);
                g_cmds.push_back(WinCmd{t, win, w, h});
            }
            char b = 1;
            ssize_t n = write(g_cmd_pipe[1], &b, 1);
            (void)n;
        });
    } else {
        wlr_log(WLR_ERROR, "wash-display: cmd pipe failed; resize/close disabled");
    }

    // Advertise xdg-decoration and force server-side, so Wayland clients
    // (GTK etc.) don't draw their own titlebar inside the wash frame.
    server.xdg_decoration = wlr_xdg_decoration_manager_v1_create(server.display);
    if (server.xdg_decoration) {
        server.new_toplevel_decoration.notify = server_new_toplevel_decoration;
        wl_signal_add(&server.xdg_decoration->events.new_toplevel_decoration,
                      &server.new_toplevel_decoration);
    }

    // Seat + virtual keyboard (DISPLAY.md §6). MUST exist before
    // wlr_xwayland_set_seat below: Xwayland's core keyboard binds to the
    // seat keymap, and without it the X server aborts ("Failed to compile
    // keymap"). The seat also carries injected pointer/keyboard input — no
    // physical devices exist on the headless backend.
    server.seat = wlr_seat_create(server.display, "seat0");
    if (server.seat) {
        wlr_seat_set_capabilities(
            server.seat, WL_SEAT_CAPABILITY_POINTER | WL_SEAT_CAPABILITY_KEYBOARD);

        static const struct wlr_keyboard_impl vkbd_impl = {
            /*name*/ "wash-vkbd", /*led_update*/ nullptr};
        wlr_keyboard_init(&server.vkbd, &vkbd_impl, "wash-vkbd");

        // Default xkb keymap (all-NULL rules → system default, typically
        // evdev/us). The keymap is layout authority; the FE only forwards
        // physical-key identity (KeyboardEvent.code → evdev keycode).
        struct xkb_context* xkb = xkb_context_new(XKB_CONTEXT_NO_FLAGS);
        struct xkb_rule_names rules{};
        struct xkb_keymap* keymap =
            xkb ? xkb_keymap_new_from_names(xkb, &rules, XKB_KEYMAP_COMPILE_NO_FLAGS)
                : nullptr;
        if (keymap) {
            wlr_keyboard_set_keymap(&server.vkbd, keymap);
            xkb_keymap_unref(keymap);
        } else {
            wlr_log(WLR_ERROR, "wash-display: xkb keymap compile failed — keys disabled");
        }
        if (xkb) xkb_context_unref(xkb);

        wlr_seat_set_keyboard(server.seat, &server.vkbd);
        server.vkbd_inited = (keymap != nullptr);

        // Forward the virtual keyboard's key/modifier signals to the seat so
        // injected keys carry correct modifier state (tinywl pattern).
        server.vkbd_key.notify = vkbd_handle_key;
        wl_signal_add(&server.vkbd.events.key, &server.vkbd_key);
        server.vkbd_modifiers.notify = vkbd_handle_modifiers;
        wl_signal_add(&server.vkbd.events.modifiers, &server.vkbd_modifiers);
        wlr_log(WLR_INFO, "wash-display: seat0 up (pointer+keyboard, vkbd=%d)",
                (int)server.vkbd_inited);

        // Clipboard bridge (DISPLAY.md §7). g_server lets the compositor-
        // thread drain install the wash→guest source; the two seat
        // listeners cover guest→wash (incl. X11 via xwm). clipboard.changed
        // (reader thread) is marshalled to the compositor thread.
        g_server = &server;
        server.req_set_selection.notify = handle_request_set_selection;
        wl_signal_add(&server.seat->events.request_set_selection,
                      &server.req_set_selection);
        server.set_selection.notify = handle_set_selection;
        wl_signal_add(&server.seat->events.set_selection, &server.set_selection);
        conn.on_clipboard_changed([](const std::string& mime) { post_clip_offer(mime); });

        // cursor-shape-v1: forward named cursors to the focused window's
        // element as a CSS cursor (M4). Modern toolkits + recent Xwayland
        // use this; bitmap cursors (request_set_cursor) are deferred (M4b).
        server.cursor_shape_mgr = wlr_cursor_shape_manager_v1_create(server.display, 1);
        if (server.cursor_shape_mgr) {
            server.cursor_shape_request.notify = handle_cursor_shape;
            wl_signal_add(&server.cursor_shape_mgr->events.request_set_shape,
                          &server.cursor_shape_request);
        }
    } else {
        wlr_log(WLR_ERROR, "wash-display: seat create failed — input disabled");
    }

#ifdef WASH_DISPLAY_XWAYLAND
    // Bring up Xwayland lazily (the X server only starts when an X client
    // first connects). Each X window arrives as a wlr_xwayland_surface on
    // events.new_surface and rides the same WindowSink pipeline as xdg.
    server.xwayland = wlr_xwayland_create(server.display, compositor, true);
    if (server.xwayland) {
        server.new_xwayland_surface.notify = server_new_xwayland_surface;
        wl_signal_add(&server.xwayland->events.new_surface,
                      &server.new_xwayland_surface);
        // Point X clients (including a WASH_DISPLAY_EXEC guest) at our X
        // server; display_name is assigned at create time even in lazy mode.
        setenv("DISPLAY", server.xwayland->display_name, 1);
        // Hand Xwayland the seat so its core keyboard binds to our keymap.
        wlr_xwayland_set_seat(server.xwayland, server.seat);
        wlr_log(WLR_INFO, "wash-display: Xwayland ready on DISPLAY=%s",
                server.xwayland->display_name);
    } else {
        wlr_log(WLR_ERROR,
                "wash-display: Xwayland init failed — X11 apps unavailable");
    }
#else
    (void)compositor;
#endif

    // Give the headless backend one virtual output so clients see a display.
    wlr_headless_add_output(server.backend, output_w(), output_h());

    const char* socket = wl_display_add_socket_auto(server.display);
    if (!socket) {
        std::fprintf(stderr, "wash-display: wl_display_add_socket_auto failed\n");
        wlr_backend_destroy(server.backend);
        return 1;
    }
    setenv("WAYLAND_DISPLAY", socket, 1);

    if (!wlr_backend_start(server.backend)) {
        std::fprintf(stderr, "wash-display: backend start failed\n");
        wlr_backend_destroy(server.backend);
        wl_display_destroy(server.display);
        return 1;
    }

    wlr_log(WLR_INFO, "wash-display: compositor up on WAYLAND_DISPLAY=%s", socket);

    // Publish the Wayland socket name back to the router so other wash
    // apps (e.g. wash-term) can point clients at us, and optionally
    // spawn a configured guest app that connects immediately.
    conn.send_app_msg(0, json{{"kind", "display_ready"},
                              {"wayland_display", socket}});

    // Publish the socket names as WASH_*-namespaced env hints. The router
    // merges these into every app it later spawns; wash-term maps them to
    // DISPLAY / WAYLAND_DISPLAY so a client typed at a wash prompt finds
    // this compositor. Gated router-side by the env-publish capability.
    // See docs/DISPLAY_ENV.md.
    {
        json pub;
        pub["WASH_WAYLAND_DISPLAY"] = socket;
        pub["WASH_DISPLAY_DPI"] = std::to_string(g_output_dpi.load());
        pub["WASH_DISPLAY_WIDTH"] = std::to_string(output_w());
        pub["WASH_DISPLAY_HEIGHT"] = std::to_string(output_h());
        pub["WASH_DISPLAY_SCALE"] = std::to_string(output_scale());
        if (const char* xrd = std::getenv("XDG_RUNTIME_DIR"); xrd && *xrd) {
            pub["WASH_XDG_RUNTIME_DIR"] = xrd;
        }
#ifdef WASH_DISPLAY_XWAYLAND
        if (server.xwayland && server.xwayland->display_name) {
            pub["WASH_X_DISPLAY"] = server.xwayland->display_name;
        }
#endif
        conn.publish_env(pub);
    }
    // Record the wayland display for the settings Display panel and push
    // the initial display.state. Window counting lives in WireConn::
    // create_window/destroy_window (the choke point both the XDG and X11
    // paths funnel through), so no per-map hooks are needed here.
    // See docs/SETTINGS.md §3b.
    conn.note_wayland_display(socket);
    maybe_spawn_guest();

    // Blocks until wl_display_terminate / fatal backend error.
    wl_display_run(server.display);

    wl_display_destroy_clients(server.display);
    wl_display_destroy(server.display);
    return 0;
}

} // namespace wash

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
#include <csignal>
#include <cstdio>
#include <cstdlib>
#include <ctime>
#include <string>
#include <unistd.h>
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
#include <wlr/types/wlr_compositor.h>
#include <wlr/types/wlr_data_device.h>
#include <wlr/types/wlr_output.h>
#include <wlr/types/wlr_output_layout.h>
#include <wlr/types/wlr_scene.h>
#include <wlr/types/wlr_subcompositor.h>
#include <wlr/types/wlr_seat.h>
#include <wlr/types/wlr_keyboard.h>
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

// Default virtual screen the headless output advertises to clients.
constexpr int kScreenW = 1280;
constexpr int kScreenH = 800;

struct Server {
    struct wl_display* display = nullptr;
    struct wlr_backend* backend = nullptr;
    struct wlr_renderer* renderer = nullptr;
    struct wlr_allocator* allocator = nullptr;
    struct wlr_scene* scene = nullptr;
    struct wlr_scene_output_layout* scene_layout = nullptr;
    struct wlr_output_layout* output_layout = nullptr;
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
struct WindowSink {
    uint32_t win = 0;        // wash window id (0 until mapped)
    uint32_t video_chan = 0; // per-window video channel (0 until opened)
    uint32_t seq = 0;        // monotonic frame counter
    SurfaceCapture cap;      // pooled BGRA capture
    SurfaceEncoder enc;      // WebP framer
    bool enc_ready = false;
};

// One mapped toplevel ↔ one wash window.
struct Toplevel {
    Server* server = nullptr;
    struct wlr_xdg_toplevel* xdg_toplevel = nullptr;
    struct wlr_scene_tree* scene_tree = nullptr;

    struct wl_listener map;
    struct wl_listener unmap;
    struct wl_listener commit;
    struct wl_listener destroy;

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
                      uint32_t w, uint32_t h) {
    s.win = conn->create_window(title, w, h);
    wlr_log(WLR_INFO, "wash-display: mapped \"%s\" %ux%u -> win=%u",
            title.c_str(), w, h, s.win);
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
                       struct wlr_renderer* renderer) {
    if (!s.win || !s.video_chan) return; // not mapped / no sink
    if (!s.cap.capture(surface, renderer)) return;

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
        wlr_log(WLR_INFO, "wash-display: win=%u frame seq=%u (%zu B) %dx%d",
                s.win, s.seq, frame.size(), s.cap.width(), s.cap.height());
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

void output_frame(struct wl_listener* listener, void* /*data*/) {
    Output* out = wl_container_of(listener, out, frame);
    wlr_scene_output_commit(out->scene_output, nullptr);
    struct timespec now;
    clock_gettime(CLOCK_MONOTONIC, &now);
    wlr_scene_output_send_frame_done(out->scene_output, &now);
}

void output_destroy(struct wl_listener* listener, void* /*data*/) {
    Output* out = wl_container_of(listener, out, destroy);
    wl_list_remove(&out->frame.link);
    wl_list_remove(&out->destroy.link);
    delete out;
}

void server_new_output(struct wl_listener* listener, void* data) {
    Server* server = wl_container_of(listener, server, new_output);
    auto* wlr_output = static_cast<struct wlr_output*>(data);

    wlr_output_init_render(wlr_output, server->allocator, server->renderer);

    struct wlr_output_state state;
    wlr_output_state_init(&state);
    wlr_output_state_set_enabled(&state, true);
    // Headless outputs have no fixed modes; set a custom mode.
    wlr_output_state_set_custom_mode(&state, kScreenW, kScreenH, 0);
    wlr_output_commit_state(wlr_output, &state);
    wlr_output_state_finish(&state);

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

void toplevel_map(struct wl_listener* listener, void* /*data*/) {
    Toplevel* t = wl_container_of(listener, t, map);

    const char* title = t->xdg_toplevel->title;
    std::string ttl = title ? title : "Window";

    struct wlr_box geo{};
    wlr_xdg_surface_get_geometry(t->xdg_toplevel->base, &geo);
    uint32_t w = geo.width > 0 ? (uint32_t)geo.width : (uint32_t)kScreenW;
    uint32_t h = geo.height > 0 ? (uint32_t)geo.height : (uint32_t)kScreenH;

    // Blocking wire round-trip; safe here (compositor thread, not the
    // WireConn reader thread).
    sink_open(t->sink, t->server->conn, ttl, w, h);
}

void toplevel_unmap(struct wl_listener* listener, void* /*data*/) {
    Toplevel* t = wl_container_of(listener, t, unmap);
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
    // Capture the just-committed buffer → WebP → one framed message on
    // the video channel (shared sink path; same as the X11 surfaces).
    sink_frame(t->sink, t->server->conn, t->xdg_toplevel->base->surface,
               t->server->renderer);
}

void toplevel_destroy(struct wl_listener* listener, void* /*data*/) {
    Toplevel* t = wl_container_of(listener, t, destroy);
    sink_close(t->sink, t->server->conn);
    wl_list_remove(&t->map.link);
    wl_list_remove(&t->unmap.link);
    wl_list_remove(&t->commit.link);
    wl_list_remove(&t->destroy.link);
    delete t;
}

// --- xdg-decoration: force server-side -----------------------------
//
// A client (e.g. GTK) that supports xdg-decoration asks the compositor
// whether it should draw its own decorations. We always answer
// SERVER_SIDE: wash draws the window frame, so the client must NOT draw
// a titlebar/border (which would appear as a frame inside the wash
// window). One Decoration per toplevel; we re-force the mode on every
// client request_mode and self-clean on destroy.
struct Decoration {
    struct wlr_xdg_toplevel_decoration_v1* deco = nullptr;
    struct wl_listener request_mode;
    struct wl_listener destroy;
};

void decoration_request_mode(struct wl_listener* listener, void* /*data*/) {
    Decoration* d = wl_container_of(listener, d, request_mode);
    wlr_xdg_toplevel_decoration_v1_set_mode(
        d->deco, WLR_XDG_TOPLEVEL_DECORATION_V1_MODE_SERVER_SIDE);
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
        deco, WLR_XDG_TOPLEVEL_DECORATION_V1_MODE_SERVER_SIDE);
}

void server_new_xdg_toplevel(struct wl_listener* listener, void* data) {
    Server* server = wl_container_of(listener, server, new_xdg_toplevel);
    // 0.17: wlr_xdg_shell has no new_toplevel signal; it emits new_surface
    // carrying a wlr_xdg_surface of any role. Take only toplevels here
    // (popups are parented by their toplevel's scene tree, not wash
    // windows); 0.18's new_toplevel did this filtering for us.
    auto* xdg_surface = static_cast<struct wlr_xdg_surface*>(data);
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
    bool surface_listeners = false; // map/unmap/commit currently wired

    WindowSink sink;
};

void xsurface_map(struct wl_listener* listener, void* /*data*/) {
    XSurface* x = wl_container_of(listener, x, map);
    const char* title = x->xsurf->title;
    std::string ttl = title ? title : "X11 Window";
    uint32_t w = x->xsurf->width  > 0 ? (uint32_t)x->xsurf->width  : (uint32_t)kScreenW;
    uint32_t h = x->xsurf->height > 0 ? (uint32_t)x->xsurf->height : (uint32_t)kScreenH;
    // X clients aren't sized implicitly by us — configure them to their
    // own requested geometry so they paint at the expected dimensions.
    wlr_xwayland_surface_configure(x->xsurf, x->xsurf->x, x->xsurf->y,
                                   (uint16_t)w, (uint16_t)h);
    sink_open(x->sink, x->server->conn, ttl, w, h);
}

void xsurface_unmap(struct wl_listener* listener, void* /*data*/) {
    XSurface* x = wl_container_of(listener, x, unmap);
    sink_close(x->sink, x->server->conn);
}

void xsurface_commit(struct wl_listener* listener, void* /*data*/) {
    XSurface* x = wl_container_of(listener, x, commit);
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
    sink_close(x->sink, x->server->conn);
    xsurface_drop_surface_listeners(x);
}

void xsurface_destroy(struct wl_listener* listener, void* /*data*/) {
    XSurface* x = wl_container_of(listener, x, destroy);
    sink_close(x->sink, x->server->conn);
    xsurface_drop_surface_listeners(x);
    wl_list_remove(&x->associate.link);
    wl_list_remove(&x->dissociate.link);
    wl_list_remove(&x->destroy.link);
    delete x;
}

void server_new_xwayland_surface(struct wl_listener* listener, void* data) {
    Server* server = wl_container_of(listener, server, new_xwayland_surface);
    auto* xsurf = static_cast<struct wlr_xwayland_surface*>(data);

    auto* x = new XSurface();
    x->server = server;
    x->xsurf = xsurf;

    // associate/dissociate bracket the inner wlr_surface's validity;
    // destroy is the X window going away. (override_redirect popups are
    // treated as plain windows in v1 — role/popup mapping is a follow-up.)
    x->associate.notify = xsurface_associate;
    wl_signal_add(&xsurf->events.associate, &x->associate);
    x->dissociate.notify = xsurface_dissociate;
    wl_signal_add(&xsurf->events.dissociate, &x->dissociate);
    x->destroy.notify = xsurface_destroy;
    wl_signal_add(&xsurf->events.destroy, &x->destroy);
}
#endif // WASH_DISPLAY_XWAYLAND

} // namespace

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
    server.scene = wlr_scene_create();
    server.scene_layout =
        wlr_scene_attach_output_layout(server.scene, server.output_layout);

    server.new_output.notify = server_new_output;
    wl_signal_add(&server.backend->events.new_output, &server.new_output);

    server.xdg_shell = wlr_xdg_shell_create(server.display, 3);
    server.new_xdg_toplevel.notify = server_new_xdg_toplevel;
    // 0.17: subscribe to new_surface (no new_toplevel signal); the handler
    // filters for the toplevel role.
    wl_signal_add(&server.xdg_shell->events.new_surface, &server.new_xdg_toplevel);

    // Advertise xdg-decoration and force server-side, so Wayland clients
    // (GTK etc.) don't draw their own titlebar inside the wash frame.
    server.xdg_decoration = wlr_xdg_decoration_manager_v1_create(server.display);
    if (server.xdg_decoration) {
        server.new_toplevel_decoration.notify = server_new_toplevel_decoration;
        wl_signal_add(&server.xdg_decoration->events.new_toplevel_decoration,
                      &server.new_toplevel_decoration);
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
    wlr_headless_add_output(server.backend, kScreenW, kScreenH);

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
    maybe_spawn_guest();

    // Blocks until wl_display_terminate / fatal backend error.
    wl_display_run(server.display);

    wl_display_destroy_clients(server.display);
    wl_display_destroy(server.display);
    return 0;
}

} // namespace wash

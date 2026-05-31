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
#include <wlr/types/wlr_xdg_shell.h>
#include <wlr/util/box.h>
#include <wlr/util/log.h>
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

    WireConn* conn = nullptr;
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

    uint32_t win = 0;        // wash window id (0 until mapped)
    uint32_t video_chan = 0; // per-window video channel (0 until opened)
    uint32_t seq = 0;        // monotonic frame counter

    SurfaceCapture cap;      // pooled BGRA capture
    SurfaceEncoder enc;      // WebP framer
    bool enc_ready = false;
};

// now_ms returns a monotonic millisecond timestamp for the WS frame
// header's ready_ts (latency stat only).
static uint64_t now_ms() {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

// maybe_spawn_guest fork+execs $WASH_DISPLAY_EXEC (if set) as a child;
// it inherits WAYLAND_DISPLAY + XDG_RUNTIME_DIR from our env, so the
// app connects straight to this compositor. Children are auto-reaped
// (SIGCHLD ignored). This is the DISPLAY.md §2 "wash-display spawns the
// guest apps" model — discovery is free.
static void maybe_spawn_guest() {
    const char* exec = std::getenv("WASH_DISPLAY_EXEC");
    if (!exec || !*exec) return;
    std::signal(SIGCHLD, SIG_IGN);
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
    t->win = t->server->conn->create_window(ttl, w, h);
    wlr_log(WLR_INFO, "wash-display: toplevel mapped \"%s\" %ux%u -> win=%u",
            ttl.c_str(), w, h, t->win);
    if (t->win) {
        // Bump the live window count → fresh display.state to the
        // settings panel.
        t->server->conn->note_window_delta(+1);
    }

    // Open the per-window video channel. Needs a bound shell (browser);
    // without one the router replies "no shell attached" and we get 0 —
    // capture still runs but frames are dropped until a shell binds.
    if (t->win) {
        t->video_chan = t->server->conn->open_video_channel(t->win);
        if (t->video_chan)
            wlr_log(WLR_INFO, "wash-display: win=%u video channel=%u", t->win, t->video_chan);
        else
            wlr_log(WLR_INFO, "wash-display: win=%u no video channel yet (no shell?)", t->win);
    }
}

void toplevel_unmap(struct wl_listener* listener, void* /*data*/) {
    Toplevel* t = wl_container_of(listener, t, unmap);
    if (t->win) {
        t->server->conn->destroy_window(t->win);
        t->server->conn->note_window_delta(-1);
        wlr_log(WLR_INFO, "wash-display: toplevel unmapped win=%u", t->win);
        t->win = 0;
    }
}

void toplevel_commit(struct wl_listener* listener, void* /*data*/) {
    Toplevel* t = wl_container_of(listener, t, commit);
    // On the initial commit the client wants a configure; let it pick
    // its own size by configuring 0x0.
    if (t->xdg_toplevel->base->initial_commit) {
        wlr_xdg_toplevel_set_size(t->xdg_toplevel, 0, 0);
        return;
    }
    if (!t->win || !t->video_chan) return; // not mapped / no sink

    // Capture the just-committed buffer → WebP → one framed message on
    // the video channel (ClassBulk via write_channel). This is the
    // capture.cpp + encode.cpp seam of the per-window pipeline.
    struct wlr_surface* surface = t->xdg_toplevel->base->surface;
    if (!t->cap.capture(surface, t->server->renderer)) return;

    if (!t->enc_ready || t->enc.width() != t->cap.width() ||
        t->enc.height() != t->cap.height()) {
        t->enc_ready = t->enc.init(t->cap.width(), t->cap.height());
        if (!t->enc_ready) return;
    }

    std::vector<uint8_t> frame = t->enc.encode_frame(
        t->cap.data(), t->cap.stride(), t->cap.width(), t->cap.height(),
        t->cap.dirty_x, t->cap.dirty_y, t->cap.dirty_w, t->cap.dirty_h, now_ms());
    if (frame.empty()) return;

    t->server->conn->write_channel(t->video_chan, frame.data(), frame.size());
    if ((++t->seq % 60) == 1) {
        wlr_log(WLR_INFO, "wash-display: win=%u frame seq=%u (%zu B) %dx%d",
                t->win, t->seq, frame.size(), t->cap.width(), t->cap.height());
    }
}

void toplevel_destroy(struct wl_listener* listener, void* /*data*/) {
    Toplevel* t = wl_container_of(listener, t, destroy);
    if (t->win) {
        // Still mapped at destroy (no unmap fired first) — drop the
        // window and its count. If unmap already ran, t->win is 0 and
        // the count was decremented there; no double-count.
        t->server->conn->destroy_window(t->win);
        t->server->conn->note_window_delta(-1);
        t->win = 0;
    }
    wl_list_remove(&t->map.link);
    wl_list_remove(&t->unmap.link);
    wl_list_remove(&t->commit.link);
    wl_list_remove(&t->destroy.link);
    delete t;
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
    // Record the wayland display for the settings Display panel and push
    // the initial display.state (running, 0 windows yet).
    conn.note_wayland_display(socket);
    maybe_spawn_guest();

    // Blocks until wl_display_terminate / fatal backend error.
    wl_display_run(server.display);

    wl_display_destroy_clients(server.display);
    wl_display_destroy(server.display);
    return 0;
}

} // namespace wash

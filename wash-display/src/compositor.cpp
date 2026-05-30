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

// WLR_USE_UNSTABLE is defined by the build (CMake). wlroots' headers
// pull in generated protocol headers (xdg-shell-protocol.h) that CMake
// generates with wayland-scanner into the build include dir.
//
// 0.17's render/scene headers declare functions with C99 "array with
// static bound" parameters (e.g. `const float color[static 4]`), which
// is not valid C++ and won't parse under our C++ compiler. We only call
// the scene/xdg API, never these float-array render entry points, so
// neutralize the `static` qualifier for the duration of these C headers
// (0.18 dropped this old render API; the project targets system 0.17).
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

#include <cstdio>
#include <cstdlib>
#include <string>

namespace wash {

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

    uint32_t win = 0; // wash window id (0 until mapped)
};

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
    // COMPOSITOR SEAM (commit 8): open a kind=video channel for t->win
    // and start streaming this surface's captured frames.
}

void toplevel_unmap(struct wl_listener* listener, void* /*data*/) {
    Toplevel* t = wl_container_of(listener, t, unmap);
    if (t->win) {
        t->server->conn->destroy_window(t->win);
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
    }
}

void toplevel_destroy(struct wl_listener* listener, void* /*data*/) {
    Toplevel* t = wl_container_of(listener, t, destroy);
    if (t->win) {
        t->server->conn->destroy_window(t->win);
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

    // Blocks until wl_display_terminate / fatal backend error.
    wl_display_run(server.display);

    wl_display_destroy_clients(server.display);
    wl_display_destroy(server.display);
    return 0;
}

} // namespace wash

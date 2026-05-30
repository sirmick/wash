// wash-display — native X/Wayland surfaces as wash windows.
//
// THIS FILE is the entry point and the wash-side integration: it dials
// the control socket, does the identity handshake, and then either
//   (a) runs the wlroots compositor (when built with wlroots — each
//       real toplevel becomes a wash window, COMPILE-gated by
//       WASH_DISPLAY_COMPOSITOR), or
//   (b) serves the fake "display_open" reference path (no wlroots) that
//       the contract e2e (e2e/tests/display-cpp.spec.ts) drives — it
//       still proves "the BE can be any language; it's just JSON wire."
//
// The threaded wire I/O lives in WireConn (wire_conn.*); the compositor
// in compositor.* . Build: cmake (see ../CMakeLists.txt).

#include "wire_conn.hpp"

#ifdef WASH_DISPLAY_COMPOSITOR
#include "compositor.hpp"
#endif

#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <thread>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

static const char* kAppID = "com.wash.display";
static const char* kVersion = "0.8.0";
static const int kProto = 1;

// --- manifest probe -------------------------------------------------
// `wash-display --wash-manifest` prints the ProbeOutput envelope the
// router caches at registration (internal/wire/manifest.go). Background
// surface: no window of its own; it creates windows on demand and they
// mount the "wash-app-display" decoder element. No FE bundle yet.
static int print_manifest() {
    std::printf(
        "{\"manifest\":{"
        "\"id\":\"%s\","
        "\"name\":\"Wash Display\","
        "\"version\":\"%s\","
        "\"protocol_version\":%d,"
        "\"element\":\"wash-app-display\","
        "\"surface\":\"background\","
        "\"icon\":\"\","
        "\"instancing\":\"singleton\","
        "\"capabilities\":[\"windows\",\"env-publish\"]"
        "},\"bundle_b64\":\"\"}\n",
        kAppID, kVersion, kProto);
    return 0;
}

// dial connects to the control socket named by $WASH_DISPLAY.
static int dial(const char* path) {
    int fd = ::socket(AF_UNIX, SOCK_STREAM, 0);
    if (fd < 0) return -1;
    sockaddr_un addr{};
    addr.sun_family = AF_UNIX;
    std::strncpy(addr.sun_path, path, sizeof(addr.sun_path) - 1);
    if (::connect(fd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) < 0) {
        ::close(fd);
        return -1;
    }
    return fd;
}

// handle_display_open is the fake reference path: create n windows and
// reply display_opened. Runs on its OWN thread (create_window blocks on
// the reader thread, so it must not run inside the app_msg callback).
static void handle_display_open(wash::WireConn& conn, const wash::json& data) {
    std::string id = data.value("id", "");
    uint64_t n = data.value("n", 1ULL);
    wash::json windows = wash::json::array();
    for (uint64_t i = 0; i < n; ++i) {
        uint32_t win = conn.create_window("Display " + std::to_string(i + 1), 320, 240);
        std::fprintf(stderr, "wash-display: created win=%u\n", win);
        windows.push_back({{"win", win}});
    }
    conn.send_app_msg(0, {{"kind", "display_opened"}, {"id", id}, {"windows", windows}});
}

static int run() {
    const char* sock = std::getenv("WASH_DISPLAY");
    if (!sock || !*sock) {
        std::fprintf(stderr, "wash-display: WASH_DISPLAY not set (run via the router)\n");
        return 1;
    }
    const char* envApp = std::getenv("WASH_APP_ID");
    std::string appID = (envApp && *envApp) ? envApp : kAppID;
    const char* envTok = std::getenv("WASH_ATTACH_TOKEN");
    std::string token = envTok ? envTok : "";

    int fd = dial(sock);
    if (fd < 0) {
        std::fprintf(stderr, "wash-display: dial %s failed\n", sock);
        return 1;
    }

    wash::WireConn conn(fd);
    std::string instanceID;
    if (!conn.handshake(appID, kVersion, kProto, token, instanceID)) {
        std::fprintf(stderr, "wash-display: handshake failed\n");
        return 1;
    }
    std::fprintf(stderr, "wash-display: attached instance=%s\n", instanceID.c_str());

    // The fake reference path: react to display_open by spawning a
    // worker that creates windows (kept even with the compositor so the
    // contract e2e keeps working as a smoke test).
    conn.on_app_msg([&conn](const wash::json& data, uint32_t /*win*/) {
        if (data.value("kind", "") == "display_open") {
            std::thread(handle_display_open, std::ref(conn), data).detach();
        }
    });

    conn.start();

#ifdef WASH_DISPLAY_COMPOSITOR
    // Real path: run the wlroots compositor on this thread. Each mapped
    // toplevel becomes a wash window via conn.create_window().
    int rc = wash::run_compositor(conn);
    conn.stop();
    return rc;
#else
    // Contract-reference path: idle until the socket dies.
    while (conn.alive()) {
        std::this_thread::sleep_for(std::chrono::milliseconds(100));
    }
    conn.stop();
    return 0;
#endif
}

int main(int argc, char** argv) {
    if (argc >= 2 && std::strcmp(argv[1], "--wash-manifest") == 0) {
        return print_manifest();
    }
    return run();
}

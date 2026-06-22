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

#include <wash/wire_conn.hpp>
#include <wash/probe.hpp>

#ifdef WASH_DISPLAY_COMPOSITOR
#include "compositor.hpp"
#endif

#include <cerrno>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <thread>
#include <csignal>
#include <execinfo.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

static const char* kAppID = "com.wash.display";
static const char* kVersion = "0.9.3";
static const int kProto = 1;

// crash_handler dumps a native backtrace to stderr on a fatal signal so
// the cause of a compositor abort/segfault lands in the router log (the
// compositor shares the router's stderr). wlroots aborts on protocol
// violations via abort()/SIGABRT, which the kernel does NOT record in
// dmesg — without this the process just vanishes silently. Re-raises with
// the default disposition so the exit status still reflects the signal.
static void crash_handler(int sig) {
    void* frames[64];
    int n = backtrace(frames, 64);
    const char* name = (sig == SIGSEGV) ? "SIGSEGV"
                     : (sig == SIGABRT) ? "SIGABRT"
                     : (sig == SIGBUS)  ? "SIGBUS"
                     : (sig == SIGFPE)  ? "SIGFPE"  : "signal";
    std::fprintf(stderr, "wash-display: FATAL %s — backtrace (%d frames):\n", name, n);
    std::fflush(stderr);
    backtrace_symbols_fd(frames, n, STDERR_FILENO);
    std::signal(sig, SIG_DFL);
    std::raise(sig);
}

// term_handler logs an orderly termination signal so a router-driven
// shutdown (SIGTERM) or a hangup is distinguishable in the log from a
// crash. Uses raw write() (async-signal-safe) and _exit().
static void term_handler(int sig) {
    const char* name = (sig == SIGTERM) ? "SIGTERM"
                     : (sig == SIGHUP)  ? "SIGHUP" : "signal";
    static const char pfx[] = "wash-display: received ";
    static const char sfx[] = " — exiting\n";
    (void)!write(STDERR_FILENO, pfx, sizeof pfx - 1);
    (void)!write(STDERR_FILENO, name, std::strlen(name));
    (void)!write(STDERR_FILENO, sfx, sizeof sfx - 1);
    _exit(128 + sig);
}

static void install_crash_handler() {
    // A socket server MUST ignore SIGPIPE: when a write() races the peer
    // closing its end (e.g. the router tearing down our connection as an
    // X client exits), the default disposition silently kills the whole
    // process — no backtrace, no run_compositor return. With SIG_IGN the
    // write() returns EPIPE and WireConn fails the frame gracefully.
    std::signal(SIGPIPE, SIG_IGN);
    std::signal(SIGSEGV, crash_handler);
    std::signal(SIGABRT, crash_handler);
    std::signal(SIGBUS, crash_handler);
    std::signal(SIGFPE, crash_handler);
    std::signal(SIGTERM, term_handler);
    std::signal(SIGHUP, term_handler);
}

// wash_panel_js is the settings "Display" panel bundle (panel.js),
// embedded as raw bytes by CMake from fe/dist/panel.js (panel_bundle.cpp,
// base64-free). Empty when the FE wasn't built.
extern const unsigned char wash_panel_js[];
extern const unsigned int wash_panel_js_len;

// --- manifest probe -------------------------------------------------
// `wash-display --wash-manifest` writes the FRAMED probe output the
// router parses (internal/wire ReadProbe): a single header JSON line,
// then the raw bundle bytes — no base64, byte-identical to the Go SDK's
// wire.WriteProbe. Background surface: no window of its own; it creates
// windows on demand (they mount the "wash-app-display" decoder element)
// and supplies the settings "Display" panel.
static int print_manifest() {
    wash::json manifest = {
        {"id", kAppID},
        {"name", "Wash Display"},
        {"version", kVersion},
        {"protocol_version", kProto},
        {"element", "wash-app-display"},
        {"surface", "background"},
        {"icon", ""},
        {"instancing", "singleton"},
        {"capabilities", {"windows", "env-publish"}},
        {"settings_panel", {{"section", "Display"}, {"element", "wash-settings-panel-display"}}},
    };
    wash::write_probe(manifest, {{"panel", wash_panel_js, wash_panel_js_len}});
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
    // Identity line first: stale-binary debugging keeps recurring, and
    // version+proto in the log is what settles "which build is this?".
    std::fprintf(stderr, "wash-display: starting version=%s proto=%d pid=%d\n",
                 kVersion, kProto, static_cast<int>(::getpid()));
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
        std::fprintf(stderr, "wash-display: dial %s failed: %s\n", sock, std::strerror(errno));
        return 1;
    }

    wash::WireConn conn(fd);
    std::string instanceID;
    if (!conn.handshake(appID, kVersion, kProto, token, instanceID)) {
        std::fprintf(stderr,
                     "wash-display: handshake failed (app_id=%s version=%s proto=%d token_present=%d)\n",
                     appID.c_str(), kVersion, kProto, token.empty() ? 0 : 1);
        return 1;
    }
    std::fprintf(stderr, "wash-display: attached instance=%s\n", instanceID.c_str());

    // App-msg dispatch (reader thread). Two consumers:
    //   - subscribe: the settings Display panel asks for a state
    //     snapshot. emit_display_state is a fire-and-forget send, safe
    //     from the reader thread (unlike create_window, which blocks).
    //   - display_open: the fake reference path (contract e2e) — create
    //     windows on a worker thread (create_window blocks the reader).
    conn.on_app_msg([&conn](const wash::json& data, uint32_t /*win*/,
                            const std::string& from) {
        const std::string kind = data.value("kind", "");
        if (kind == "subscribe") {
            // The settings Display panel subscribes. Record it (so window
            // deltas push it fresh state) and reply with a snapshot.
            conn.add_subscriber(from);
            conn.emit_display_state();
        } else if (kind == "unsubscribe") {
            conn.remove_subscriber(from);
        } else if (kind == "display_open") {
            std::thread(handle_display_open, std::ref(conn), data).detach();
        }
#ifdef WASH_DISPLAY_COMPOSITOR
        else if (kind == "input") {
            // FE <wash-app-display> → injected pointer/keyboard input
            // (DISPLAY.md §6). post_input only enqueues + wakes the
            // compositor thread (no blocking), so it's safe from here on
            // the reader thread. Target window is data["win"].
            wash::post_input(data);
        }
#endif
    });

    conn.start();

#ifdef WASH_DISPLAY_COMPOSITOR
    // Real path: run the wlroots compositor on this thread. Each mapped
    // toplevel becomes a wash window via conn.create_window().
    int rc = wash::run_compositor(conn);
    std::fprintf(stderr, "wash-display: run_compositor returned rc=%d (alive=%d) — "
                         "compositor event loop exited\n", rc, (int)conn.alive());
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
    install_crash_handler();
    return run();
}

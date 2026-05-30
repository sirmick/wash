// wash-display — native X/Wayland surfaces as wash windows.
//
// THIS FILE is the wash-side integration: it speaks the wash wire
// protocol (docs/WIRE.md) over the dialed control socket, does the
// identity handshake, and drives the multi-window contract
// (docs/DISPLAY.md §4) — window.create / window.created / app_msg —
// all in C++, proving "the BE can be any language; it's just a JSON
// wire protocol."
//
// The COMPOSITOR half (wlroots: real Wayland/X11 surfaces, per-surface
// capture) and the ENCODE half (libdatachannel/codecs, copied from
// mac-phoenix) attach at the seams marked `COMPOSITOR SEAM` below.
// Those translation units require wlroots + libdatachannel and are
// gated separately so this wire client builds on its own. Until they
// exist, display_open is driven as a contract reference (creates the
// windows; the per-window video channel needs a bound shell).
//
// Build: cmake (see ../CMakeLists.txt). No wlroots dependency here.

#include "wire.hpp"
#include "json.hpp"

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <vector>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

using wash::Frame;

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
        "\"capabilities\":[\"windows\"]"
        "},\"bundle_b64\":\"\"}\n",
        kAppID, kVersion, kProto);
    return 0;
}

// --- wire helpers ---------------------------------------------------
static bool send_json(int fd, uint32_t channel, const std::string& json) {
    Frame f;
    f.flags = wash::FLAG_END;
    f.channel = channel;
    f.payload = json;
    return wash::write_frame(fd, f);
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

// handshake: send identity (with pid; router validates /proc/pid/exe),
// read identity.ack.
static bool handshake(int fd, const std::string& appID, const std::string& token,
                      std::string& instanceID, uint64_t& windowID) {
    std::string id = "{\"t\":\"identity\",\"app_id\":\"" + wash::json::esc(appID) +
                     "\",\"proto\":" + std::to_string(kProto) +
                     ",\"version\":\"" + kVersion + "\",\"pid\":" + std::to_string(::getpid());
    if (!token.empty()) id += ",\"attach_token\":\"" + wash::json::esc(token) + "\"";
    id += "}";
    if (!send_json(fd, wash::CH_CONTROL, id)) return false;

    Frame f;
    if (!wash::read_frame(fd, f)) return false;
    std::string t;
    if (!wash::json::find_string(f.payload, "t", t) || t != "identity.ack") {
        std::fprintf(stderr, "wash-display: expected identity.ack, got %s\n", f.payload.c_str());
        return false;
    }
    wash::json::find_string(f.payload, "instance_id", instanceID);
    windowID = 0;
    wash::json::find_uint(f.payload, "window_id", windowID); // absent for background
    return true;
}

// create_window sends window.create and blocks until the matching
// window.created (docs/DISPLAY.md §4). Returns 0 on failure.
static uint64_t create_window(int fd, uint64_t& reqSeq, const std::string& title) {
    uint64_t req = ++reqSeq;
    std::string m = "{\"t\":\"window.create\",\"req_id\":" + std::to_string(req) +
                    ",\"role\":\"toplevel\",\"title\":\"" + wash::json::esc(title) +
                    "\",\"w\":320,\"h\":240}";
    if (!send_json(fd, wash::CH_EVENT, m)) return 0;
    for (;;) {
        Frame f;
        if (!wash::read_frame(fd, f)) return 0;
        if (f.channel != wash::CH_EVENT) continue;
        std::string t;
        if (!wash::json::find_string(f.payload, "t", t)) continue;
        if (t == "window.created") {
            uint64_t rid = 0, win = 0;
            wash::json::find_uint(f.payload, "req_id", rid);
            if (rid != req) continue;
            wash::json::find_uint(f.payload, "win", win);
            return win;
        }
        if (t == "window.create.err") {
            uint64_t rid = 0;
            wash::json::find_uint(f.payload, "req_id", rid);
            if (rid == req) {
                std::fprintf(stderr, "wash-display: window.create.err: %s\n", f.payload.c_str());
                return 0;
            }
        }
    }
}

// on_display_open creates n windows and replies display_opened. This is
// the contract reference until the COMPOSITOR SEAM is filled: there,
// each Wayland/X11 toplevel triggers create_window, and a per-window
// video channel (OpenChannel kind=video) carries encoded frames.
static void on_display_open(int fd, uint64_t& reqSeq, const std::string& data) {
    std::string id;
    wash::json::find_string(data, "id", id);
    uint64_t n = 1;
    wash::json::find_uint(data, "n", n);

    std::string wins;
    for (uint64_t i = 0; i < n; ++i) {
        uint64_t win = create_window(fd, reqSeq, "Display " + std::to_string(i + 1));
        std::fprintf(stderr, "wash-display: created win=%llu\n", (unsigned long long)win);
        if (!wins.empty()) wins += ",";
        wins += "{\"win\":" + std::to_string(win) + "}";
        // COMPOSITOR SEAM: open kind=video channel for `win` and stream
        // wlroots-captured + libdatachannel-encoded frames here.
    }
    std::string reply = "{\"t\":\"app_msg\",\"win\":0,\"data\":{\"kind\":\"display_opened\",\"id\":\"" +
                        wash::json::esc(id) + "\",\"windows\":[" + wins + "]}}";
    send_json(fd, wash::CH_EVENT, reply);
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
    std::string instanceID;
    uint64_t windowID = 0;
    if (!handshake(fd, appID, token, instanceID, windowID)) {
        std::fprintf(stderr, "wash-display: handshake failed\n");
        return 1;
    }
    std::fprintf(stderr, "wash-display: attached instance=%s\n", instanceID.c_str());

    // COMPOSITOR SEAM: start the wlroots compositor + Xwayland here, on
    // its own event loop/thread; each new toplevel surface calls
    // create_window() and opens a per-window video channel.

    uint64_t reqSeq = 0;
    for (;;) {
        Frame f;
        if (!wash::read_frame(fd, f)) break; // socket closed → exit
        if (f.channel != wash::CH_EVENT) continue;
        std::string t;
        if (!wash::json::find_string(f.payload, "t", t)) continue;
        if (t == "app_msg") {
            std::string data = wash::json::data_object(f.payload);
            std::string kind;
            if (wash::json::find_string(data, "kind", kind) && kind == "display_open") {
                on_display_open(fd, reqSeq, data);
            }
        } else if (t == "shutdown") {
            break;
        }
    }
    ::close(fd);
    return 0;
}

int main(int argc, char** argv) {
    if (argc >= 2 && std::strcmp(argv[1], "--wash-manifest") == 0) {
        return print_manifest();
    }
    return run();
}

// cpp-about — the minimal C++ wash app, built on cpp-sdk. A window that
// shows the BE⇄FE app_msg round-trip: the BE greets the FE on connect,
// the FE sends a signal on a button click, the BE echoes it back. Copy
// this directory to start a new native (C++) wash app.
//
// Build:  make           (cmake; embeds assets/index.js as the FE bundle)
// Probe:  ./out/cpp-about --wash-manifest

#include <wash/probe.hpp>
#include <wash/wire_conn.hpp>

#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <thread>

#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

// index.js embedded as raw bytes by CMake (main_bundle.cpp) — the FE
// bundle, shipped raw in the framed probe (no base64).
extern const unsigned char wash_index_js[];
extern const unsigned int wash_index_js_len;

static const char* kAppID = "com.wash.examples.cppabout";
static const char* kVersion = "0.1.0";
static const int kProto = 1;

static const char* kIcon =
    "data:image/svg+xml,<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 24 24\" "
    "fill=\"none\" stroke=\"%2390a0e0\" stroke-width=\"2\" stroke-linecap=\"round\">"
    "<rect x=\"3\" y=\"4\" width=\"18\" height=\"16\" rx=\"2\"/><path d=\"M8 9l3 3-3 3M13 15h3\"/></svg>";

// print_manifest writes the framed --wash-manifest output: header line +
// the raw FE bundle. wash::write_probe is the C++ mirror of Go's
// wire.WriteProbe.
static int print_manifest() {
    wash::json manifest = {
        {"id", kAppID},
        {"name", "C++ About (example)"},
        {"version", kVersion},
        {"protocol_version", kProto},
        {"element", "wash-app-cpp-about"},
        {"surface", "window"},
        {"icon", kIcon},
        {"instancing", "multi"},
        {"window", {{"default_width", 440}, {"default_height", 340}}},
    };
    wash::write_probe(manifest, {{"main", wash_index_js, wash_index_js_len}});
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

int main(int argc, char** argv) {
    if (argc >= 2 && std::strcmp(argv[1], "--wash-manifest") == 0) {
        return print_manifest();
    }

    const char* sock = std::getenv("WASH_DISPLAY");
    if (!sock || !*sock) {
        std::fprintf(stderr, "cpp-about: WASH_DISPLAY not set (run via the router)\n");
        return 1;
    }
    const char* envApp = std::getenv("WASH_APP_ID");
    std::string appID = (envApp && *envApp) ? envApp : kAppID;
    const char* envTok = std::getenv("WASH_ATTACH_TOKEN");
    std::string token = envTok ? envTok : "";

    int fd = dial(sock);
    if (fd < 0) {
        std::fprintf(stderr, "cpp-about: dial %s failed\n", sock);
        return 1;
    }

    wash::WireConn conn(fd);
    std::string instanceID;
    if (!conn.handshake(appID, kVersion, kProto, token, instanceID)) {
        std::fprintf(stderr, "cpp-about: handshake failed\n");
        return 1;
    }
    std::fprintf(stderr, "cpp-about: attached instance=%s\n", instanceID.c_str());

    // receive: the FE sent us a signal — echo it straight back.
    conn.on_app_msg([&conn](const wash::json& data, uint32_t /*win*/, const std::string& /*from*/) {
        conn.send_app_msg(0, {{"kind", "echo"}, {"from_fe", data}});
    });
    conn.start();

    // transmit: greet the FE once we're attached.
    conn.send_app_msg(0, {{"kind", "hello"}, {"msg", "hello from the C++ BE"}});

    // Idle until the router closes the socket.
    while (conn.alive()) {
        std::this_thread::sleep_for(std::chrono::milliseconds(100));
    }
    conn.stop();
    return 0;
}

// wash-display threaded wire connection.
//
// The original main.cpp drove the wire synchronously: it read frames
// inline while blocking for a window.created reply. That cannot coexist
// with a wlroots compositor running its own event loop on another
// thread (docs/DISPLAY.md §6: input/frames cross threads). WireConn
// owns the socket, runs ONE reader thread that dispatches every inbound
// frame, and turns the request/reply messages (window.create,
// channel.open) into blocking calls fulfilled by the reader via a
// per-req_id condvar. Writes are serialized by a single mutex.
//
// JSON is parsed/produced with nlohmann/json (third_party/) now that the
// message set (app_msg with nested input-event arrays, etc.) has
// outgrown the hand-rolled json.hpp.
#pragma once

#include "wire.hpp"
#include <nlohmann/json.hpp>

#include <atomic>
#include <condition_variable>
#include <cstdint>
#include <functional>
#include <map>
#include <mutex>
#include <string>
#include <thread>

namespace wash {

using json = nlohmann::json;

class WireConn {
public:
    // Called on the reader thread for each inbound app_msg. `data` is
    // the nested "data" object; `win` is the message's "win" (0 if
    // absent). MUST NOT call create_window/open_video_channel directly
    // (that blocks on this same thread → deadlock); offload to another
    // thread. Same rule the Go SDK documents.
    using AppMsgHandler = std::function<void(const json& data, uint32_t win)>;

    explicit WireConn(int fd) : fd_(fd) {}
    ~WireConn();

    WireConn(const WireConn&) = delete;
    WireConn& operator=(const WireConn&) = delete;

    // handshake sends identity and waits for identity.ack. Synchronous,
    // call BEFORE start(). Returns false on protocol/socket error.
    bool handshake(const std::string& appID, const std::string& version,
                   int proto, const std::string& token, std::string& instanceID);

    // start launches the reader thread; stop joins it. stop() is also
    // run by the destructor.
    void start();
    void stop();

    // create_window sends window.create and blocks for window.created /
    // window.create.err. Returns the window id, or 0 on failure.
    // Blocking: never call from the AppMsgHandler.
    uint32_t create_window(const std::string& title, uint32_t w, uint32_t h,
                           const std::string& role = "toplevel", uint32_t parent = 0);

    // destroy_window is fire-and-forget (window.destroy on CH_EVENT).
    void destroy_window(uint32_t win);

    // report_geometry tells the router a window's content changed size
    // (window.geometry on CH_EVENT, fire-and-forget) so the shell frame
    // tracks the new size. Send only on actual size change.
    void report_geometry(uint32_t win, uint32_t w, uint32_t h);

    // open_video_channel sends channel.open{kind:"video"} for `win` and
    // blocks for channel.opened / channel.open.err. Returns the
    // allocated channel id, or 0 on failure (e.g. "no shell attached"
    // when no browser is bound yet). Blocking: not from AppMsgHandler.
    uint32_t open_video_channel(uint32_t win);

    // write_channel writes one raw frame (e.g. a framed video message)
    // on an already-open channel id.
    bool write_channel(uint32_t channelID, const uint8_t* data, size_t n);

    // send_app_msg emits an app_msg event carrying `data` for window
    // `win` (0 = instance level).
    bool send_app_msg(uint32_t win, const json& data);

    // publish_env sends env.publish: WASH_*-namespaced env hints the
    // router merges into every app it later spawns (docs/DISPLAY_ENV.md),
    // so wash-term's shell can reach DISPLAY / WAYLAND_DISPLAY. Requires
    // the env-publish capability (declared in the manifest).
    bool publish_env(const json& env);

    void on_app_msg(AppMsgHandler h) { app_msg_handler_ = std::move(h); }

    // alive is false once the socket closed or a shutdown arrived.
    bool alive() const { return alive_.load(); }

private:
    void reader_loop();
    bool write_json(uint32_t channel, const json& j);
    uint64_t next_req() { return req_seq_.fetch_add(1) + 1; }

    int fd_;
    std::thread reader_;
    std::atomic<bool> running_{false};
    std::atomic<bool> alive_{true};
    std::atomic<uint64_t> req_seq_{0};

    std::mutex write_mu_;

    struct Reply {
        bool done = false;
        bool ok = false;
        uint32_t value = 0; // win id or channel id
    };

    std::mutex win_mu_;
    std::condition_variable win_cv_;
    std::map<uint64_t, Reply> win_pending_;

    std::mutex chan_mu_;
    std::condition_variable chan_cv_;
    std::map<uint64_t, Reply> chan_pending_;

    AppMsgHandler app_msg_handler_;
};

} // namespace wash

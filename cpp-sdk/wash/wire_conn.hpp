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
#include <set>
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
    using AppMsgHandler =
        std::function<void(const json& data, uint32_t win, const std::string& from)>;

    // Called on the reader thread for router→app window commands the
    // compositor must act on: "window.resize" (w,h set) and
    // "window.close_requested" (w,h zero). Like AppMsgHandler this runs
    // on the reader thread and MUST NOT call wlroots directly — the
    // compositor marshals onto its own thread (wlroots is single-thread).
    using WindowCmdHandler =
        std::function<void(const std::string& t, uint32_t win, uint32_t w, uint32_t h)>;

    // Called on the reader thread when the router-held clipboard changes
    // (some OTHER app called clipboard.set — the setter is excluded). The
    // clipboard bridge uses this to claim the Wayland/X11 selection so a
    // guest can paste wash's clipboard (DISPLAY.md §7). MUST NOT block /
    // call clipboard_get directly here (the reply arrives on this thread);
    // offload, or marshal onto the compositor thread.
    using ClipboardChangedHandler = std::function<void(const std::string& mime)>;

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

    // confirm_close replies to a router window.close_requested. We pass
    // allow=false to VETO the router's auto-destroy: the compositor asks
    // the guest client to close politely and drives the real teardown via
    // destroy_window when the client actually exits (window.confirm_close).
    void confirm_close(uint32_t win, bool allow);

    // open_video_channel sends channel.open{kind:"video"} for `win` and
    // blocks for channel.opened / channel.open.err. Returns the
    // allocated channel id, or 0 on failure (e.g. "no shell attached"
    // when no browser is bound yet). Blocking: not from AppMsgHandler.
    uint32_t open_video_channel(uint32_t win);

    // open_channel_kind is open_video_channel with an explicit kind hint
    // (e.g. "video-popup" for a child-surface overlay channel bound to a
    // PARENT window). Same blocking/reply semantics. DISPLAY.md §12 (M3).
    uint32_t open_channel_kind(uint32_t win, const std::string& kind);

    // write_channel writes one raw frame (e.g. a framed video message)
    // on an already-open channel id.
    bool write_channel(uint32_t channelID, const uint8_t* data, size_t n);

    // send_app_msg emits an app_msg event carrying `data` for window
    // `win` (0 = instance level).
    bool send_app_msg(uint32_t win, const json& data);

    // send_app_msg_to addresses a specific instance (e.g. the settings
    // panel that subscribed) via app_msg.to{instance_id}.
    bool send_app_msg_to(const std::string& instanceID, const json& data);

    // --- clipboard bridge (DISPLAY.md §7) -----------------------------
    // clipboard_set stores (mime, bytes) in the router-held clipboard
    // (clipboard.set on CH_EVENT, fire-and-forget). Used for guest→wash
    // copies. Bytes are base64'd on the JSON wire (matching the Go SDK's
    // []byte encoding); see [[wash_cbor_json_pitfall]].
    void clipboard_set(const std::string& mime, const std::vector<uint8_t>& data);

    // clipboard_get fetches the current clipboard (clipboard.get → blocks
    // for clipboard.data). Used for wash→guest paste. Returns false on
    // socket death. Blocking: never call from the reader-thread callbacks
    // (AppMsgHandler / ClipboardChangedHandler) — deadlocks the reply.
    bool clipboard_get(std::string& mime, std::vector<uint8_t>& data);

    void on_clipboard_changed(ClipboardChangedHandler h) {
        clipboard_changed_handler_ = std::move(h);
    }

    // publish_env sends env.publish: WASH_*-namespaced env hints the
    // router merges into every app it later spawns (docs/DISPLAY_ENV.md),
    // so wash-term's shell can reach DISPLAY / WAYLAND_DISPLAY. Requires
    // the env-publish capability (declared in the manifest).
    bool publish_env(const json& env);

    // --- display.state (settings Display panel, docs/SETTINGS.md §3b) -
    // The compositor reports its live state so the settings panel can
    // render status; the panel subscribes for a snapshot, then a fresh
    // push on every window open/close. All three are fire-and-forget
    // send_app_msg, safe from either thread.
    void note_wayland_display(const std::string& wd);
    void note_window_delta(int d);
    void emit_display_state();
    // add/remove_subscriber track which instances asked for display.state.
    // A background service has no FE of its own, so replies are addressed
    // to the subscriber's instance (send_app_msg_to), not send_app_msg(0).
    void add_subscriber(const std::string& instanceID);
    void remove_subscriber(const std::string& instanceID);

    void on_app_msg(AppMsgHandler h) { app_msg_handler_ = std::move(h); }
    void on_window_cmd(WindowCmdHandler h) { window_cmd_handler_ = std::move(h); }

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
    WindowCmdHandler window_cmd_handler_;
    ClipboardChangedHandler clipboard_changed_handler_;

    // clipboard.get req/reply, mirroring win_pending_/chan_pending_.
    struct ClipReply {
        bool done = false;
        bool ok = false;
        std::string mime;
        std::vector<uint8_t> data;
    };
    std::mutex clip_mu_;
    std::condition_variable clip_cv_;
    std::map<uint64_t, ClipReply> clip_pending_;

    // display.state snapshot. state_mu_ guards the string; the count is
    // atomic so map/unmap (compositor thread) and a subscribe (reader
    // thread) don't race.
    std::mutex state_mu_;
    std::string wayland_display_;
    std::atomic<int> window_count_{0};
    std::set<std::string> subs_; // display.state subscribers (guarded by state_mu_)
};

} // namespace wash

#include "wire_conn.hpp"

#include <cstdio>
#include <cstdint>
#include <string>
#include <vector>
#include <sys/socket.h>
#include <unistd.h>

namespace wash {

namespace {
// Minimal RFC 4648 base64. The router base64s clipboard byte fields on the
// JSON wire (encoding/json's default for []byte), so the C++ side must
// match — JSON has no native byte type. (See [[wash_cbor_json_pitfall]].)
constexpr char kB64[] =
    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

std::string b64_encode(const std::vector<uint8_t>& in) {
    std::string out;
    out.reserve(((in.size() + 2) / 3) * 4);
    size_t i = 0;
    for (; i + 3 <= in.size(); i += 3) {
        uint32_t n = (in[i] << 16) | (in[i + 1] << 8) | in[i + 2];
        out += kB64[(n >> 18) & 63];
        out += kB64[(n >> 12) & 63];
        out += kB64[(n >> 6) & 63];
        out += kB64[n & 63];
    }
    if (size_t rem = in.size() - i; rem == 1) {
        uint32_t n = in[i] << 16;
        out += kB64[(n >> 18) & 63];
        out += kB64[(n >> 12) & 63];
        out += "==";
    } else if (rem == 2) {
        uint32_t n = (in[i] << 16) | (in[i + 1] << 8);
        out += kB64[(n >> 18) & 63];
        out += kB64[(n >> 12) & 63];
        out += kB64[(n >> 6) & 63];
        out += '=';
    }
    return out;
}

std::vector<uint8_t> b64_decode(const std::string& in) {
    auto val = [](char c) -> int {
        if (c >= 'A' && c <= 'Z') return c - 'A';
        if (c >= 'a' && c <= 'z') return c - 'a' + 26;
        if (c >= '0' && c <= '9') return c - '0' + 52;
        if (c == '+') return 62;
        if (c == '/') return 63;
        return -1; // padding / whitespace / invalid
    };
    std::vector<uint8_t> out;
    out.reserve(in.size() / 4 * 3);
    int acc = 0, bits = 0;
    for (char c : in) {
        int v = val(c);
        if (v < 0) continue; // skip '=' and any stray bytes
        acc = (acc << 6) | v;
        bits += 6;
        if (bits >= 8) {
            bits -= 8;
            out.push_back((uint8_t)((acc >> bits) & 0xFF));
        }
    }
    return out;
}

FrameClass class_for_channel_kind(const std::string& kind) {
    if (kind == "video" || kind == "video-popup") return FrameClass::Bulk;
    return FrameClass::Interactive;
}
} // namespace

WireConn::~WireConn() {
    stop();
}

bool WireConn::write_json(uint32_t channel, const json& j) {
    std::string s = j.dump();
    Frame f;
    f.flags = FLAG_END;
    f.channel = channel;
    f.payload = std::move(s);
    std::lock_guard<std::mutex> lk(write_mu_);
    return write_frame(fd_, f);
}

bool WireConn::handshake(const std::string& appID, const std::string& version,
                         int proto, const std::string& token, std::string& instanceID) {
    json id = {
        {"t", "identity"},
        {"app_id", appID},
        {"proto", proto},
        {"version", version},
        {"pid", static_cast<int>(::getpid())},
    };
    if (!token.empty()) id["attach_token"] = token;
    if (!write_json(CH_CONTROL, id)) return false;

    Frame f;
    if (!read_frame(fd_, f)) return false;
    json m = json::parse(f.payload, nullptr, false);
    if (m.is_discarded() || m.value("t", "") != "identity.ack") {
        std::fprintf(stderr, "wash-display: expected identity.ack, got %s\n",
                     f.payload.c_str());
        return false;
    }
    instanceID = m.value("instance_id", "");
    return true;
}

void WireConn::start() {
    running_.store(true);
    reader_ = std::thread([this] { reader_loop(); });
}

void WireConn::stop() {
    running_.store(false);
    if (reader_.joinable()) {
        // Closing the fd unblocks the reader's blocking read().
        ::shutdown(fd_, SHUT_RDWR);
        reader_.join();
    }
}

void WireConn::reader_loop() {
    for (;;) {
        Frame f;
        if (!read_frame(fd_, f)) break; // socket closed
        if (!running_.load()) break;

        // Raw channels (>=2) carry opaque bytes (inbound video unused in
        // v1). Only control(0)/event(1) carry JSON we dispatch.
        if (f.channel != CH_CONTROL && f.channel != CH_EVENT) continue;

        json m = json::parse(f.payload, nullptr, false);
        if (m.is_discarded()) continue;
        const std::string t = m.value("t", "");

        if (f.channel == CH_CONTROL) {
            if (t == "channel.opened") {
                uint64_t rid = m.value("req_id", 0ULL);
                uint32_t cid = m.value("channel_id", 0U);
                std::lock_guard<std::mutex> lk(chan_mu_);
                auto it = chan_pending_.find(rid);
                if (it != chan_pending_.end()) {
                    it->second = {true, true, cid};
                    chan_cv_.notify_all();
                }
            } else if (t == "channel.open.err") {
                uint64_t rid = m.value("req_id", 0ULL);
                std::fprintf(stderr, "wash-display: channel.open.err: %s\n",
                             f.payload.c_str());
                std::lock_guard<std::mutex> lk(chan_mu_);
                auto it = chan_pending_.find(rid);
                if (it != chan_pending_.end()) {
                    it->second = {true, false, 0};
                    chan_cv_.notify_all();
                }
            } else if (t == "channel.closed") {
                uint32_t cid = m.value("channel_id", 0U);
                if (cid) {
                    std::lock_guard<std::mutex> lk(chan_mu_);
                    chan_class_.erase(cid);
                }
            }
            continue;
        }

        // CH_EVENT
        if (t == "window.created") {
            uint64_t rid = m.value("req_id", 0ULL);
            uint32_t win = m.value("win", 0U);
            std::lock_guard<std::mutex> lk(win_mu_);
            auto it = win_pending_.find(rid);
            if (it != win_pending_.end()) {
                it->second = {true, true, win};
                win_cv_.notify_all();
            }
        } else if (t == "window.create.err") {
            uint64_t rid = m.value("req_id", 0ULL);
            std::fprintf(stderr, "wash-display: window.create.err: %s\n",
                         f.payload.c_str());
            std::lock_guard<std::mutex> lk(win_mu_);
            auto it = win_pending_.find(rid);
            if (it != win_pending_.end()) {
                it->second = {true, false, 0};
                win_cv_.notify_all();
            }
        } else if (t == "app_msg") {
            if (app_msg_handler_) {
                uint32_t win = m.value("win", 0U);
                json data = m.contains("data") ? m["data"] : json::object();
                // Router-attested sender (set on cross-app deliveries);
                // the display.state reply path addresses it directly.
                std::string from;
                if (m.contains("from") && m["from"].is_object())
                    from = m["from"].value("instance_id", "");
                app_msg_handler_(data, win, from);
            }
        } else if (t == "window.resize") {
            // Router → app COMMAND: the shell resized a window's frame.
            // Hand to the compositor via window_cmd_handler_, which marshals
            // onto the wlroots thread (single-threaded; cannot touch here).
            if (window_cmd_handler_) {
                uint32_t win = m.value("win", 0U);
                uint32_t w = m.value("w", 0U);
                uint32_t h = m.value("h", 0U);
                window_cmd_handler_(t, win, w, h);
            }
        } else if (t == "window.close_requested") {
            // Router → app COMMAND: user clicked the window's close button.
            // The compositor MUST respond (confirm_close) or the router's
            // grace timer force-kills the whole process (WIRE.md §10). This
            // dispatch was previously missing — close silently dropped here
            // → no veto → grace expiry SIGKILLed the compositor.
            if (window_cmd_handler_) {
                uint32_t win = m.value("win", 0U);
                window_cmd_handler_(t, win, 0, 0);
            }
        } else if (t == "clipboard.data") {
            // Reply to clipboard_get (req/reply). data is base64 on the wire.
            uint64_t rid = m.value("req_id", 0ULL);
            std::lock_guard<std::mutex> lk(clip_mu_);
            auto it = clip_pending_.find(rid);
            if (it != clip_pending_.end()) {
                it->second.ok = true;
                it->second.done = true;
                it->second.mime = m.value("mime", "");
                it->second.data = b64_decode(m.value("data", ""));
                clip_cv_.notify_all();
            }
        } else if (t == "clipboard.changed") {
            // Some other app set the clipboard; claim the guest selection so
            // a paste pulls wash's content (DISPLAY.md §7).
            if (clipboard_changed_handler_)
                clipboard_changed_handler_(m.value("mime", ""));
        } else if (t == "window.focus" || t == "window.unfocus") {
            // Router → app: the WM gave/took keyboard focus for `win`.
            // Focus is router-authoritative (docs/DISPLAY.md §6): the
            // compositor sets wl_seat keyboard focus to the matching
            // surface so injected keys land in the right window. Marshalled
            // onto the wlroots thread like the other window commands.
            if (window_cmd_handler_) {
                uint32_t win = m.value("win", 0U);
                window_cmd_handler_(t, win, 0, 0);
            }
        } else if (t == "shutdown") {
            break;
        }
        // Other window.* events are ignored for now.
    }
    alive_.store(false);
    // Wake any blocked callers so they fail instead of hanging.
    { std::lock_guard<std::mutex> lk(win_mu_); win_cv_.notify_all(); }
    { std::lock_guard<std::mutex> lk(chan_mu_); chan_cv_.notify_all(); }
    { std::lock_guard<std::mutex> lk(clip_mu_); clip_cv_.notify_all(); }
    // The wire is gone (router crash/SIGKILL/e2e teardown, or a clean
    // shutdown). Tell the owner so a compositor can terminate its wlroots loop
    // instead of running forever as an orphan (REVIEW-X11-WAYLAND #3).
    if (disconnect_handler_) disconnect_handler_();
}

uint32_t WireConn::create_window(const std::string& title, uint32_t w, uint32_t h,
                                 const std::string& role, uint32_t parent, bool chromeless) {
    if (!alive_.load()) return 0;
    uint64_t req = next_req();
    { std::lock_guard<std::mutex> lk(win_mu_); win_pending_[req] = Reply{}; }

    json m = {
        {"t", "window.create"}, {"req_id", req},
        {"role", role}, {"title", title}, {"w", w}, {"h", h},
    };
    if (parent) m["parent_win"] = parent;
    if (chromeless) m["chromeless"] = true;
    if (!write_json(CH_EVENT, m)) {
        std::lock_guard<std::mutex> lk(win_mu_);
        win_pending_.erase(req);
        return 0;
    }

    std::unique_lock<std::mutex> lk(win_mu_);
    win_cv_.wait(lk, [&] { return win_pending_[req].done || !alive_.load(); });
    Reply r = win_pending_[req];
    win_pending_.erase(req);
    lk.unlock();
    if (r.ok && r.value) {
        // Every window — XDG and X11 alike — is minted here, so this is
        // the one place to track the live count for the settings Display
        // panel (docs/SETTINGS.md §3b).
        note_window_delta(+1);
    }
    return r.ok ? r.value : 0;
}

void WireConn::destroy_window(uint32_t win) {
    json m = {{"t", "window.destroy"}, {"win", win}};
    write_json(CH_EVENT, m);
    note_window_delta(-1);
}

void WireConn::report_geometry(uint32_t win, uint32_t w, uint32_t h) {
    json m = {{"t", "window.geometry"}, {"win", win}, {"w", w}, {"h", h}};
    write_json(CH_EVENT, m);
}

void WireConn::confirm_close(uint32_t win, bool allow) {
    json m = {{"t", "window.confirm_close"}, {"win", win}, {"allow", allow}};
    write_json(CH_EVENT, m);
}

uint32_t WireConn::open_video_channel(uint32_t win) {
    return open_channel_kind(win, "video");
}

uint32_t WireConn::open_channel_kind(uint32_t win, const std::string& kind) {
    if (!alive_.load()) return 0;
    uint64_t req = next_req();
    { std::lock_guard<std::mutex> lk(chan_mu_); chan_pending_[req] = Reply{}; }

    json m = {
        {"t", "channel.open"}, {"req_id", req},
        {"window_id", win}, {"kind", kind},
    };
    if (!write_json(CH_CONTROL, m)) {
        std::lock_guard<std::mutex> lk(chan_mu_);
        chan_pending_.erase(req);
        return 0;
    }

    std::unique_lock<std::mutex> lk(chan_mu_);
    chan_cv_.wait(lk, [&] { return chan_pending_[req].done || !alive_.load(); });
    Reply r = chan_pending_[req];
    chan_pending_.erase(req);
    if (r.ok && r.value) {
        chan_class_[r.value] = class_for_channel_kind(kind);
    }
    return r.ok ? r.value : 0;
}

bool WireConn::write_channel(uint32_t channelID, const uint8_t* data, size_t n) {
    FrameClass cls = FrameClass::Interactive;
    {
        std::lock_guard<std::mutex> lk(chan_mu_);
        auto it = chan_class_.find(channelID);
        if (it != chan_class_.end()) cls = it->second;
    }
    std::lock_guard<std::mutex> lk(write_mu_);
    return write_raw_frame(fd_, flags_with_class(FLAG_END, cls), channelID, data, n);
}

bool WireConn::send_app_msg(uint32_t win, const json& data) {
    json m = {{"t", "app_msg"}, {"win", win}, {"data", data}};
    return write_json(CH_EVENT, m);
}

// send_app_msg_to addresses a specific instance (the subscribed settings
// panel). A background service has no FE half, so send_app_msg(0,…) —
// which the router routes to the sender's own FE — reaches nobody.
bool WireConn::send_app_msg_to(const std::string& instanceID, const json& data) {
    json m = {{"t", "app_msg"}, {"data", data},
              {"to", {{"instance_id", instanceID}}}};
    return write_json(CH_EVENT, m);
}

void WireConn::add_subscriber(const std::string& instanceID) {
    if (instanceID.empty()) return;
    std::lock_guard<std::mutex> lk(state_mu_);
    subs_.insert(instanceID);
}

void WireConn::remove_subscriber(const std::string& instanceID) {
    std::lock_guard<std::mutex> lk(state_mu_);
    subs_.erase(instanceID);
}

bool WireConn::publish_env(const json& env) {
    json m = {{"t", "env.publish"}, {"env", env}};
    return write_json(CH_EVENT, m);
}

void WireConn::clipboard_set(const std::string& mime, const std::vector<uint8_t>& data) {
    json m = {{"t", "clipboard.set"}, {"mime", mime}, {"data", b64_encode(data)}};
    write_json(CH_EVENT, m);
}

bool WireConn::clipboard_get(std::string& mime, std::vector<uint8_t>& data) {
    if (!alive_.load()) return false;
    uint64_t req = next_req();
    { std::lock_guard<std::mutex> lk(clip_mu_); clip_pending_[req] = ClipReply{}; }

    json m = {{"t", "clipboard.get"}, {"req_id", req}};
    if (!write_json(CH_EVENT, m)) {
        std::lock_guard<std::mutex> lk(clip_mu_);
        clip_pending_.erase(req);
        return false;
    }

    std::unique_lock<std::mutex> lk(clip_mu_);
    clip_cv_.wait(lk, [&] { return clip_pending_[req].done || !alive_.load(); });
    ClipReply r = std::move(clip_pending_[req]);
    clip_pending_.erase(req);
    lk.unlock();
    if (!r.ok) return false;
    mime = std::move(r.mime);
    data = std::move(r.data);
    return true;
}

void WireConn::note_wayland_display(const std::string& wd) {
    {
        std::lock_guard<std::mutex> lk(state_mu_);
        wayland_display_ = wd;
    }
    emit_display_state();
}

void WireConn::note_display_dpi(int dpi) {
    if (dpi <= 0) dpi = 96;
    display_dpi_.store(dpi);
    emit_display_state();
}

void WireConn::note_display_metrics(uint32_t width, uint32_t height, uint32_t scale) {
    if (width == 0) width = 1920;
    if (height == 0) height = 1080;
    if (scale == 0) scale = 1;
    display_width_.store(width);
    display_height_.store(height);
    display_scale_.store(scale);
    emit_display_state();
}

void WireConn::note_window_delta(int d) {
    window_count_.fetch_add(d);
    emit_display_state();
}

void WireConn::emit_display_state() {
    json payload;
    std::vector<std::string> targets;
    {
        std::lock_guard<std::mutex> lk(state_mu_);
        int n = window_count_.load();
        if (n < 0) n = 0;
        // running is always true: only a live process can answer. The
        // panel maps running->"running", an absent reply->"not installed".
        payload = json{{"kind", "display.state"},
                       {"running", true},
                       {"wayland_display", wayland_display_},
                       {"window_count", n},
                       {"dpi", display_dpi_.load()},
                       {"display_width", display_width_.load()},
                       {"display_height", display_height_.load()},
                       {"display_scale", display_scale_.load()}};
        targets.assign(subs_.begin(), subs_.end());
    }
    for (const auto& id : targets) send_app_msg_to(id, payload);
}

} // namespace wash

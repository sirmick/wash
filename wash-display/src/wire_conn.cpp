#include "wire_conn.hpp"

#include <cstdio>
#include <sys/socket.h>
#include <unistd.h>

namespace wash {

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
                app_msg_handler_(data, win);
            }
        } else if (t == "shutdown") {
            break;
        }
        // Other window.* events (focus/resize/close_requested) are
        // consumed by the compositor in later commits; ignored here.
    }
    alive_.store(false);
    // Wake any blocked callers so they fail instead of hanging.
    { std::lock_guard<std::mutex> lk(win_mu_); win_cv_.notify_all(); }
    { std::lock_guard<std::mutex> lk(chan_mu_); chan_cv_.notify_all(); }
}

uint32_t WireConn::create_window(const std::string& title, uint32_t w, uint32_t h,
                                 const std::string& role, uint32_t parent) {
    if (!alive_.load()) return 0;
    uint64_t req = next_req();
    { std::lock_guard<std::mutex> lk(win_mu_); win_pending_[req] = Reply{}; }

    json m = {
        {"t", "window.create"}, {"req_id", req},
        {"role", role}, {"title", title}, {"w", w}, {"h", h},
    };
    if (parent) m["parent_win"] = parent;
    if (!write_json(CH_EVENT, m)) {
        std::lock_guard<std::mutex> lk(win_mu_);
        win_pending_.erase(req);
        return 0;
    }

    std::unique_lock<std::mutex> lk(win_mu_);
    win_cv_.wait(lk, [&] { return win_pending_[req].done || !alive_.load(); });
    Reply r = win_pending_[req];
    win_pending_.erase(req);
    return r.ok ? r.value : 0;
}

void WireConn::destroy_window(uint32_t win) {
    json m = {{"t", "window.destroy"}, {"win", win}};
    write_json(CH_EVENT, m);
}

uint32_t WireConn::open_video_channel(uint32_t win) {
    if (!alive_.load()) return 0;
    uint64_t req = next_req();
    { std::lock_guard<std::mutex> lk(chan_mu_); chan_pending_[req] = Reply{}; }

    json m = {
        {"t", "channel.open"}, {"req_id", req},
        {"window_id", win}, {"kind", "video"},
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
    return r.ok ? r.value : 0;
}

bool WireConn::write_channel(uint32_t channelID, const uint8_t* data, size_t n) {
    Frame f;
    f.flags = FLAG_END;
    f.channel = channelID;
    f.payload.assign(reinterpret_cast<const char*>(data), n);
    std::lock_guard<std::mutex> lk(write_mu_);
    return write_frame(fd_, f);
}

bool WireConn::send_app_msg(uint32_t win, const json& data) {
    json m = {{"t", "app_msg"}, {"win", win}, {"data", data}};
    return write_json(CH_EVENT, m);
}

bool WireConn::publish_env(const json& env) {
    json m = {{"t", "env.publish"}, {"env", env}};
    return write_json(CH_EVENT, m);
}

} // namespace wash

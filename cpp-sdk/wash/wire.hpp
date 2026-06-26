// wash wire protocol — minimal C++ client.
//
// Frame layout (docs/WIRE.md §2, matches internal/wire/frame.go):
//   [flags:8][channel-id:24 BE][length:32 BE][payload:length]
// flags: bit0 END (always set in v0.1), bits1-2 class. Channel 0 =
// control (JSON), channel 1 = event (JSON), >=2 = raw bytes.
//
// This header is deliberately dependency-free (POSIX sockets + stdlib)
// so the wire client builds without wlroots/libdatachannel — those
// only enter the compositor/encode translation units.
#pragma once

#include <cstdint>
#include <string>
#include <vector>
#include <unistd.h>

namespace wash {

constexpr uint8_t FLAG_END = 0x01;
constexpr uint8_t FLAG_CLASS_MASK = 0x06;
constexpr uint8_t FLAG_CLASS_SHIFT = 1;
constexpr uint32_t CH_CONTROL = 0;
constexpr uint32_t CH_EVENT = 1;

enum class FrameClass : uint8_t {
    Interactive = 0,
    Bulk = 1,
    Background = 2,
    Control = 3,
};

inline uint8_t flags_with_class(uint8_t flags, FrameClass cls) {
    return static_cast<uint8_t>((flags & ~FLAG_CLASS_MASK) |
                                ((static_cast<uint8_t>(cls) << FLAG_CLASS_SHIFT) &
                                 FLAG_CLASS_MASK));
}

struct Frame {
    uint8_t flags = FLAG_END;
    uint32_t channel = 0;
    std::string payload; // JSON for ch0/ch1, raw bytes for ch>=2
};

// readn/writen: full read/write of n bytes over a blocking fd.
inline bool readn(int fd, void* buf, size_t n) {
    auto* p = static_cast<uint8_t*>(buf);
    while (n > 0) {
        ssize_t r = ::read(fd, p, n);
        if (r <= 0) return false;
        p += r;
        n -= static_cast<size_t>(r);
    }
    return true;
}
inline bool writen(int fd, const void* buf, size_t n) {
    const auto* p = static_cast<const uint8_t*>(buf);
    while (n > 0) {
        ssize_t w = ::write(fd, p, n);
        if (w <= 0) return false;
        p += w;
        n -= static_cast<size_t>(w);
    }
    return true;
}

inline bool write_frame(int fd, const Frame& f) {
    uint8_t hdr[8];
    hdr[0] = f.flags;
    hdr[1] = static_cast<uint8_t>(f.channel >> 16);
    hdr[2] = static_cast<uint8_t>(f.channel >> 8);
    hdr[3] = static_cast<uint8_t>(f.channel);
    uint32_t len = static_cast<uint32_t>(f.payload.size());
    hdr[4] = static_cast<uint8_t>(len >> 24);
    hdr[5] = static_cast<uint8_t>(len >> 16);
    hdr[6] = static_cast<uint8_t>(len >> 8);
    hdr[7] = static_cast<uint8_t>(len);
    if (!writen(fd, hdr, 8)) return false;
    if (len == 0) return true;
    return writen(fd, f.payload.data(), len);
}

inline bool write_raw_frame(int fd, uint8_t flags, uint32_t channel,
                            const uint8_t* data, size_t n) {
    if (n > 0xFFFFFFFFu) return false;
    uint8_t hdr[8];
    hdr[0] = flags;
    hdr[1] = static_cast<uint8_t>(channel >> 16);
    hdr[2] = static_cast<uint8_t>(channel >> 8);
    hdr[3] = static_cast<uint8_t>(channel);
    uint32_t len = static_cast<uint32_t>(n);
    hdr[4] = static_cast<uint8_t>(len >> 24);
    hdr[5] = static_cast<uint8_t>(len >> 16);
    hdr[6] = static_cast<uint8_t>(len >> 8);
    hdr[7] = static_cast<uint8_t>(len);
    if (!writen(fd, hdr, 8)) return false;
    if (len == 0) return true;
    return writen(fd, data, n);
}

inline bool read_frame(int fd, Frame& f) {
    uint8_t hdr[8];
    if (!readn(fd, hdr, 8)) return false;
    f.flags = hdr[0];
    f.channel = (uint32_t(hdr[1]) << 16) | (uint32_t(hdr[2]) << 8) | uint32_t(hdr[3]);
    uint32_t len = (uint32_t(hdr[4]) << 24) | (uint32_t(hdr[5]) << 16) |
                   (uint32_t(hdr[6]) << 8) | uint32_t(hdr[7]);
    f.payload.resize(len);
    if (len == 0) return true;
    return readn(fd, &f.payload[0], len);
}

} // namespace wash

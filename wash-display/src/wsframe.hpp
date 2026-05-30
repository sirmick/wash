// wash-display WS video-frame header.
//
// The mac-phoenix WebSocket image-stream framing (docs/DISPLAY.md §5):
// a 45-byte LITTLE-ENDIAN header is prepended to the encoded payload and
// sent as ONE message on the wash "video" channel. The browser
// <wash-app-display> decoder (ported from mac-phoenix client.js) parses
// exactly these offsets, so this layout is a hard contract verified
// against client.js (PNGDecoder.handleData) and webrtc_server.cpp:
//
//   off  size  field          type
//   0    8     t1_frame_ready u64   (ms; latency stat, may be 0)
//   8    4     dirty_x        u32
//   12   4     dirty_y        u32
//   16   4     dirty_width    u32
//   20   4     dirty_height   u32
//   24   4     frame_width    u32   (full surface width)
//   28   4     frame_height   u32   (full surface height)
//   32   8     t4_send_time   u64   (ms; latency stat, may be 0)
//   40   2     cursor_x       u16
//   42   2     cursor_y       u16
//   44   1     cursor_visible u8
//   45   ...   payload              (codec inferred from magic bytes:
//                                    PNG 0x89.., WebP "RIFF"…"WEBP")
//
// NOTE: there is NO codec-id, sequence, or payload-length field — the
// WS message boundary delimits the payload and the browser's
// createImageBitmap() autodetects PNG vs WebP from the magic bytes.
// Header-only: no link deps beyond stdlib.
#pragma once

#include "encoders/codec.h"
#include <cstdint>
#include <cstddef>
#include <cstring>
#include <vector>

namespace wash {

constexpr size_t kVideoHeaderSize = 45;

inline void put_u16_le(uint8_t* p, uint32_t v) {
    p[0] = uint8_t(v); p[1] = uint8_t(v >> 8);
}
inline void put_u32_le(uint8_t* p, uint32_t v) {
    p[0] = uint8_t(v);       p[1] = uint8_t(v >> 8);
    p[2] = uint8_t(v >> 16); p[3] = uint8_t(v >> 24);
}
inline void put_u64_le(uint8_t* p, uint64_t v) {
    for (int i = 0; i < 8; ++i) p[i] = uint8_t(v >> (8 * i));
}

// build_video_frame prepends the 45-byte LE header to f.data and returns
// one buffer ready to write as a single message on the video channel.
// Cursor fields are zeroed (wash renders its own cursor in v1).
inline std::vector<uint8_t> build_video_frame(const EncodedFrame& f,
                                              uint64_t ready_ts_ms,
                                              int frame_w, int frame_h) {
    std::vector<uint8_t> out(kVideoHeaderSize + f.data.size(), 0);
    uint8_t* h = out.data();
    put_u64_le(h + 0,  ready_ts_ms);
    put_u32_le(h + 8,  uint32_t(f.dirty_x));
    put_u32_le(h + 12, uint32_t(f.dirty_y));
    put_u32_le(h + 16, uint32_t(f.dirty_w > 0 ? f.dirty_w : frame_w));
    put_u32_le(h + 20, uint32_t(f.dirty_h > 0 ? f.dirty_h : frame_h));
    put_u32_le(h + 24, uint32_t(frame_w));
    put_u32_le(h + 28, uint32_t(frame_h));
    put_u64_le(h + 32, 0);          // t4_send_time (unused)
    // bytes 40..44 (cursor x/y/visible) left zero
    if (!f.data.empty()) {
        std::memcpy(h + kVideoHeaderSize, f.data.data(), f.data.size());
    }
    return out;
}

} // namespace wash

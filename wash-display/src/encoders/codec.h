// wash-display codec abstraction.
//
// COPIED from github.com/sirmick/mac-phoenix
// (src/drivers/video/encoders/codec.h) — see docs/DISPLAY.md §2
// ("back-half: copy large sections from mac-phoenix"). Unifies the
// image codecs (WebP now; PNG dropped) and the future WebRTC video
// codecs (VP9) behind one interface so capture→encode→transport is
// codec-agnostic.
#pragma once

#include <cstdint>
#include <cstddef>
#include <vector>
#include <string>

// Pixel format of the source framebuffer.
enum class PixelFormat {
    BGRA8888,   // wlroots/X shm default (little-endian ARGB)
    RGBA8888,
};

// Codec configuration.
struct CodecConfig {
    int width = 0;
    int height = 0;
    int target_bitrate = 0;     // kbps, for video codecs
    int quality = 75;           // 0-100, for image codecs
    bool lossless = false;
    PixelFormat format = PixelFormat::BGRA8888;
};

// One encoded output unit.
struct EncodedFrame {
    std::vector<uint8_t> data;
    bool is_keyframe = false;
    int width = 0;
    int height = 0;
    // Dirty rectangle this frame covers (image codecs).
    int dirty_x = 0, dirty_y = 0, dirty_w = 0, dirty_h = 0;
};

// Source framebuffer view (not owned).
struct FrameBuffer {
    const uint8_t* data = nullptr;
    int width = 0;
    int height = 0;
    int stride = 0;             // bytes per row
    PixelFormat format = PixelFormat::BGRA8888;
    // Dirty rectangle to encode (image codecs may encode only this sub-rect).
    int dirty_x = 0, dirty_y = 0, dirty_w = 0, dirty_h = 0;
};

// Abstract codec.
class Codec {
public:
    virtual ~Codec() = default;
    virtual bool init(const CodecConfig& cfg) = 0;
    virtual EncodedFrame encode(const FrameBuffer& fb) = 0;
    virtual void reset() = 0;
    virtual const char* name() const = 0;
};

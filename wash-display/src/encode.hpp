// wash-display per-surface encoder.
//
// Ties the vendored WebP codec (encoders/) to the mac-phoenix WS frame
// header (wsframe.hpp): given a captured BGRA surface buffer + dirty
// rect, produce ONE ready-to-send message for the wash "video" channel
// (docs/DISPLAY.md §5). One SurfaceEncoder per window. This is the
// `encode.cpp` translation unit of the compositor seam.
#pragma once

#include "encoders/codec.h"
#include "encoders/webp_encoder.h"
#include "wsframe.hpp"
#include <cstdint>
#include <vector>

namespace wash {

class SurfaceEncoder {
public:
    // init/re-init for a surface of the given full size. Safe to call
    // again when the surface resizes.
    bool init(int frame_w, int frame_h, int quality = 75);

    int width() const { return w_; }
    int height() const { return h_; }

    // encode_frame encodes the damaged sub-rect [dx,dy,dw,dh] of a
    // BGRA8888 buffer (stride bytes/row) and returns the framed video
    // message (45-byte WS header + WebP payload). Empty on failure.
    // Pass dw==0||dh==0 to encode the whole frame.
    std::vector<uint8_t> encode_frame(const uint8_t* bgra, int stride,
                                      int frame_w, int frame_h,
                                      int dx, int dy, int dw, int dh,
                                      uint64_t ready_ts_ms);

private:
    WebPEncoder enc_;
    int w_ = 0, h_ = 0;
    bool ready_ = false;
};

} // namespace wash

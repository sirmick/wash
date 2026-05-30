#include "encode.hpp"

namespace wash {

bool SurfaceEncoder::init(int frame_w, int frame_h, int quality) {
    CodecConfig cfg;
    cfg.width = frame_w;
    cfg.height = frame_h;
    cfg.quality = quality;
    cfg.lossless = false;
    cfg.format = PixelFormat::BGRA8888;  // wlroots/X shm native
    ready_ = enc_.init(cfg);
    if (ready_) { w_ = frame_w; h_ = frame_h; }
    return ready_;
}

std::vector<uint8_t> SurfaceEncoder::encode_frame(const uint8_t* bgra, int stride,
                                                  int frame_w, int frame_h,
                                                  int dx, int dy, int dw, int dh,
                                                  uint64_t ready_ts_ms) {
    if (!ready_ || !bgra) return {};

    FrameBuffer fb;
    fb.data = bgra;
    fb.width = frame_w;
    fb.height = frame_h;
    fb.stride = stride;
    fb.format = PixelFormat::BGRA8888;
    fb.dirty_x = dx; fb.dirty_y = dy; fb.dirty_w = dw; fb.dirty_h = dh;

    EncodedFrame ef = enc_.encode(fb);
    if (ef.data.empty()) return {};

    return build_video_frame(ef, ready_ts_ms, frame_w, frame_h);
}

} // namespace wash

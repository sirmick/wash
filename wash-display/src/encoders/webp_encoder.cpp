// wash-display WebP encoder — COPIED from github.com/sirmick/mac-phoenix
// (src/drivers/video/encoders/webp_encoder.cpp).
#include "webp_encoder.h"
#include <cstring>

WebPEncoder::WebPEncoder() {}

WebPEncoder::~WebPEncoder() {
    if (initialized_) {
        WebPPictureFree(&pic_);
    }
}

bool WebPEncoder::init(const CodecConfig& cfg) {
    config_ = cfg;
    if (!WebPConfigInit(&cfg_)) return false;
    cfg_.quality = static_cast<float>(cfg.quality);
    cfg_.lossless = cfg.lossless ? 1 : 0;
    cfg_.method = 4;  // 0=fast, 6=slow/best
    cfg_.thread_level = 1;
    if (!WebPValidateConfig(&cfg_)) return false;

    if (!WebPPictureInit(&pic_)) return false;
    pic_.width = cfg.width;
    pic_.height = cfg.height;
    pic_.use_argb = 1;
    initialized_ = true;
    return true;
}

EncodedFrame WebPEncoder::encode(const FrameBuffer& fb) {
    EncodedFrame out;
    // Encode only the dirty sub-rectangle when provided, else the full frame.
    int ex = fb.dirty_w > 0 ? fb.dirty_x : 0;
    int ey = fb.dirty_h > 0 ? fb.dirty_y : 0;
    int ew = fb.dirty_w > 0 ? fb.dirty_w : fb.width;
    int eh = fb.dirty_h > 0 ? fb.dirty_h : fb.height;

    WebPConfig cfg = cfg_;
    WebPPicture pic;
    if (!WebPPictureInit(&pic)) return out;
    pic.width = ew;
    pic.height = eh;
    pic.use_argb = 1;

    // Import the sub-rect. BGRA vs RGBA per source format.
    const uint8_t* base = fb.data + (size_t)ey * fb.stride + (size_t)ex * 4;
    int ok;
    if (fb.format == PixelFormat::BGRA8888) {
        ok = WebPPictureImportBGRA(&pic, base, fb.stride);
    } else {
        ok = WebPPictureImportRGBA(&pic, base, fb.stride);
    }
    if (!ok) { WebPPictureFree(&pic); return out; }

    WebPMemoryWriter wrt;
    WebPMemoryWriterInit(&wrt);
    pic.writer = WebPMemoryWrite;
    pic.custom_ptr = &wrt;

    if (WebPEncode(&cfg, &pic)) {
        out.data.assign(wrt.mem, wrt.mem + wrt.size);
        out.is_keyframe = true;  // every webp frame is self-contained
        out.width = ew; out.height = eh;
        out.dirty_x = ex; out.dirty_y = ey; out.dirty_w = ew; out.dirty_h = eh;
    }
    WebPMemoryWriterClear(&wrt);
    WebPPictureFree(&pic);
    return out;
}

void WebPEncoder::reset() {}

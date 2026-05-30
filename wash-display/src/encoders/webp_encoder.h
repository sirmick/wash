// wash-display WebP encoder.
//
// COPIED from github.com/sirmick/mac-phoenix
// (src/drivers/video/encoders/webp_encoder.h). Encodes the damaged
// sub-rectangle of a surface to a self-contained WebP image for the
// WS-tunnelled "video" channel (docs/DISPLAY.md §5). Not the WebRTC
// path — VP9 will add that later.
#pragma once
#include "codec.h"
#include <webp/encode.h>
#include <vector>
#include <cstdint>

class WebPEncoder : public Codec {
public:
    WebPEncoder();
    ~WebPEncoder() override;

    bool init(const CodecConfig& cfg) override;
    EncodedFrame encode(const FrameBuffer& fb) override;
    void reset() override;
    const char* name() const override { return "webp"; }

private:
    WebPConfig cfg_{};
    WebPPicture pic_{};
    bool initialized_ = false;
    CodecConfig config_{};
};

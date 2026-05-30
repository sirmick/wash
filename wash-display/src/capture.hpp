// wash-display per-surface capture (commit 8).
//
// Reads a mapped wlroots surface's current buffer into a CPU-side
// BGRA8888 image so the WebP encoder can frame it for the "video"
// channel (docs/DISPLAY.md §5). One SurfaceCapture per window; its
// backing buffer is POOLED and reused across frames — the Xwayland
// churn gotcha (DISPLAY.md §11) means we must never alloc per frame.
//
// Capture goes through wlr_texture_read_pixels, which works for both
// wl_shm (software) and dmabuf (GPU) client buffers via the renderer —
// so a GTK app rendering with hardware accel is captured the same way
// as a software terminal.
//
// Needs wlroots; only compiled in the WASH_DISPLAY_COMPOSITOR build.
#pragma once

#include <cstdint>
#include <vector>

struct wlr_surface;
struct wlr_renderer;

namespace wash {

class SurfaceCapture {
public:
    // capture reads `surface`'s current texture into the pooled buffer.
    // Returns true on success; false if the surface has no texture yet
    // or readback failed. `renderer` is the compositor's renderer.
    bool capture(struct wlr_surface* surface, struct wlr_renderer* renderer);

    const uint8_t* data() const { return buf_.data(); }
    int width() const { return w_; }
    int height() const { return h_; }
    int stride() const { return stride_; }

    // Dirty rectangle for this frame. v1 captures full-surface, so this
    // is the whole surface; damage-tracked sub-rects are a follow-up.
    int dirty_x = 0, dirty_y = 0, dirty_w = 0, dirty_h = 0;

private:
    std::vector<uint8_t> buf_; // pooled BGRA8888, reused across frames
    int w_ = 0, h_ = 0, stride_ = 0;
    // Pooled GPU render target (struct wlr_buffer*) the client texture is
    // drawn into before read-back; grown only when the surface size changes
    // (DISPLAY.md §11 — never alloc per frame).
    void* render_buf_ = nullptr;
    int rb_w_ = 0, rb_h_ = 0;
};

} // namespace wash

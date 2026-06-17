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
#include <map>

struct wlr_surface;
struct wlr_renderer;

namespace wash {

class SurfaceCapture {
public:
    SurfaceCapture() = default;
    // Drops the pooled GPU render target so a closed window doesn't leak it.
    ~SurfaceCapture();
    SurfaceCapture(const SurfaceCapture&) = delete;            // owns a wlr_buffer
    SurfaceCapture& operator=(const SurfaceCapture&) = delete;
    // capture reads `surface`'s current texture into the pooled buffer.
    // Returns true on success; false if the surface has no texture yet
    // or readback failed. `renderer` is the compositor's renderer.
    //
    // When crop_w/crop_h are > 0, only that surface-local sub-rect is read
    // back. This strips the GTK client-side-decoration shadow margin: xdg
    // toplevels report their visible bounds via wlr_xdg_surface_get_geometry,
    // and we capture only that rect so the transparent CSD margin (which would
    // flatten to black in the alpha-less XRGB read-back) never reaches the
    // encoder. Zero/omit for the full surface (X11 apps, popups). Coords are
    // surface-local = texture pixels at scale 1.0 (DISPLAY.md — DPI fixed 1.0).
    // force_full forces a whole-frame capture (bypasses the root-surface
    // damage skip). Set it when the capture is driven by a tree-change
    // signal rather than a root commit, since subsurface-only repaints leave
    // the root surface's damage empty (DISPLAY.md M7).
    // preserve_alpha keeps the client's transparency (for SHAPED popups —
    // menus with rounded corners + shadow). For toplevel windows it must be
    // false: they are opaque, but a browser leaves stray alpha in its chrome,
    // which would otherwise show the window behind through the gaps (M8c).
    bool capture(struct wlr_surface* surface, struct wlr_renderer* renderer,
                 int crop_x = 0, int crop_y = 0, int crop_w = 0, int crop_h = 0,
                 bool force_full = false, bool preserve_alpha = false);

    const uint8_t* data() const { return buf_.data(); }
    int width() const { return w_; }
    int height() const { return h_; }
    int stride() const { return stride_; }

    // Dirty rectangle for this frame, in cropped-buffer coords. The whole
    // tree is composited, but only the union of the surfaces that actually
    // changed (see seq_) is encoded/sent — M7 tree-aware damage.
    int dirty_x = 0, dirty_y = 0, dirty_w = 0, dirty_h = 0;

private:
    std::vector<uint8_t> buf_; // pooled BGRA8888, reused across frames
    int w_ = 0, h_ = 0, stride_ = 0;
    // Pooled GPU render target (struct wlr_buffer*) the client texture is
    // drawn into before read-back; grown only when the surface size changes
    // (DISPLAY.md §11 — never alloc per frame).
    void* render_buf_ = nullptr;
    int rb_w_ = 0, rb_h_ = 0;
    // Per-surface last-seen commit seq, keyed by surface pointer. A surface
    // contributes to the dirty rect only when its seq advances, so a static
    // surface's stale last-commit damage (e.g. a browser's root/CSD layer,
    // damaged once at startup) doesn't force a full-frame encode every frame.
    std::map<struct wlr_surface*, uint32_t> seq_;
};

} // namespace wash

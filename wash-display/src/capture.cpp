// capture.cpp — pull pixels from a wlr_surface into a CPU buffer.
//
// vendored wlroots 0.17.4: there is NO wlr_texture_read_pixels. The only
// public read-back is wlr_renderer_read_pixels(), which copies from the
// renderer's currently-bound framebuffer (render/wlr_renderer.c -> the
// gles2 impl does glReadPixels on the bound FBO). A mapped client texture
// is not itself a renderable framebuffer, so we:
//   1. wlr_surface_get_texture()       -> the client's uploaded texture,
//   2. wlr_allocator_create_buffer()   -> a renderable target (pooled),
//   3. begin_buffer_pass + add_texture + submit  -> draw the texture in,
//   4. wlr_renderer_begin_with_buffer() binds that buffer's FBO, then
//      wlr_renderer_read_pixels() reads it back into our pooled CPU buffer,
//      wlr_renderer_end().
// Works for both wl_shm (software) and dmabuf (GPU) clients because the
// texture upload + GPU read-back path is renderer-agnostic.
//
// STL/C++ headers MUST precede the `#define static` hack below; otherwise
// the macro corrupts libstdc++ headers (<limits> etc.) with ~600 errors.
#include <cstdint>
#include <cstring>
#include <vector>
#include <cmath>
#include <limits>

#include "capture.hpp"

// 0.17 render/scene headers declare functions with C99 "array with static
// bound" params (e.g. `const float color[static 4]`), invalid in C++.
// Neutralize `static` only while these C headers parse.
#define static
extern "C" {
#include <wayland-server-core.h>
#include <wlr/render/wlr_renderer.h>
#include <wlr/render/wlr_texture.h>
#include <wlr/render/pass.h>
#include <wlr/render/allocator.h>
#include <wlr/render/drm_format_set.h>
#include <wlr/types/wlr_compositor.h>
#include <wlr/types/wlr_buffer.h>
#include <wlr/util/box.h>
#include <drm_fourcc.h>
#include <pixman.h>
}
#undef static

namespace wash {

// The compositor owns the allocator; capture() mints render targets from it.
// Set once in compositor.cpp after wlr_allocator_autocreate().
extern struct wlr_allocator* g_capture_allocator;

namespace {
// Composite the surface tree (root + subsurfaces) into the render target.
// wlr_surface_for_each_surface walks the whole tree giving each surface's
// offset (sx,sy) in root-surface coords; we draw each textured surface at
// (sx,sy) minus the crop origin, so subsurface content (e.g. a browser's
// web-content surface) lands in the buffer. The render pass clips to the
// target, so the root's transparent CSD shadow margin (outside the crop)
// is dropped. Surfaces without a texture (not yet committed) are skipped.
struct CompositeCtx {
    struct wlr_render_pass* pass;
    int off_x;   // crop origin x in root-surface coords
    int off_y;   // crop origin y
    int drawn;   // count of textured surfaces composited
};
void composite_surface_cb(struct wlr_surface* s, int sx, int sy, void* data) {
    auto* c = static_cast<CompositeCtx*>(data);
    struct wlr_texture* tex = wlr_surface_get_texture(s);
    if (!tex) return;
    struct wlr_render_texture_options o;
    std::memset(&o, 0, sizeof o);
    o.texture = tex;
    o.dst_box.x = sx - c->off_x;
    o.dst_box.y = sy - c->off_y;
    o.dst_box.width = (int)tex->width;
    o.dst_box.height = (int)tex->height;
    wlr_render_pass_add_texture(c->pass, &o);
    c->drawn++;
}
} // namespace

bool SurfaceCapture::capture(struct wlr_surface* surface, struct wlr_renderer* renderer,
                             int crop_x, int crop_y, int crop_w, int crop_h,
                             bool force_full) {
    if (!surface || !renderer) return false;

    struct wlr_texture* texture = wlr_surface_get_texture(surface);
    if (!texture) return false;

    const int tw = (int)texture->width;
    const int th = (int)texture->height;
    if (tw <= 0 || th <= 0) return false;

    // Resolve the capture rect. A caller-supplied crop (xdg window geometry,
    // sans the CSD shadow margin) is clamped to the texture; with no crop we
    // take the whole buffer. src_{x,y} offsets the read-back into the texture.
    int src_x = 0, src_y = 0, w = tw, h = th;
    if (crop_w > 0 && crop_h > 0) {
        src_x = crop_x < 0 ? 0 : (crop_x > tw ? tw : crop_x);
        src_y = crop_y < 0 ? 0 : (crop_y > th ? th : crop_y);
        w = crop_w > tw - src_x ? tw - src_x : crop_w;
        h = crop_h > th - src_y ? th - src_y : crop_h;
    }
    if (w <= 0 || h <= 0) return false;

    // The texture's own renderer is the one that can read it back.
    struct wlr_renderer* r = texture->renderer ? texture->renderer : renderer;

    struct wlr_buffer* render_buf = (struct wlr_buffer*)render_buf_;

    // A size change (or first capture) forces a full-frame dirty rect —
    // the FE canvas is resized and must be fully repainted.
    bool full_capture = force_full || (!render_buf || rb_w_ != w || rb_h_ != h);

    // (Re)allocate the pooled render target only when the size changes.
    if (!render_buf || rb_w_ != w || rb_h_ != h) {
        if (render_buf) {
            wlr_buffer_drop(render_buf);
            render_buf = nullptr;
            render_buf_ = nullptr;
        }
        if (!g_capture_allocator) return false;
        // The gbm allocator asserts format->len > 0 (render/allocator/gbm.c:96)
        // and special-cases a single DRM_FORMAT_MOD_LINEAR modifier into a
        // linear (CPU-readable) BO — exactly what we want for read-back.
        // wlr_drm_format_add() is private in 0.17.4, so build the one-modifier
        // format by hand. create_buffer() consumes it synchronously (gbm copies
        // what it needs), so a stack-backed modifier array is safe; do NOT call
        // wlr_drm_format_finish() on it — that would free() a stack pointer.
        uint64_t modifiers[1] = { DRM_FORMAT_MOD_LINEAR };
        struct wlr_drm_format fmt;
        std::memset(&fmt, 0, sizeof fmt);
        fmt.format = DRM_FORMAT_XRGB8888;
        fmt.len = 1;
        fmt.capacity = 1;
        fmt.modifiers = modifiers;
        render_buf = wlr_allocator_create_buffer(g_capture_allocator, w, h, &fmt);
        if (!render_buf) return false;
        render_buf_ = render_buf;
        rb_w_ = w;
        rb_h_ = h;
    }

    // Composite the surface tree (root + subsurfaces) into the render target.
    // src_x/src_y are the crop origin: each surface is drawn at its tree
    // offset minus the origin, and the pass clips to the w×h target. This is
    // what makes browsers/video (which paint into subsurfaces) capture at all,
    // and simultaneously drops the CSD shadow margin (outside the crop rect).
    struct wlr_buffer_pass_options pass_opts;
    std::memset(&pass_opts, 0, sizeof pass_opts);
    struct wlr_render_pass* pass = wlr_renderer_begin_buffer_pass(r, render_buf, &pass_opts);
    if (!pass) return false;

    CompositeCtx ctx{ pass, src_x, src_y, 0 };
    wlr_surface_for_each_surface(surface, composite_surface_cb, &ctx);
    if (!wlr_render_pass_submit(pass)) return false;
    if (ctx.drawn == 0) return false; // nothing textured yet

    // Grow-only CPU buffer (no per-frame alloc) and read the pixels back.
    stride_ = w * 4;
    size_t need = (size_t)stride_ * (size_t)h;
    if (buf_.size() < need) buf_.resize(need);

    // Read back as XBGR8888 (-> GL_RGBA in the gles2 format table), NOT
    // XRGB8888 (-> GL_BGRA_EXT): the latter needs GL_EXT_read_format_bgra,
    // which Mesa's surfaceless/llvmpipe GLES2 context does not advertise, so
    // glReadPixels would fail ("missing GL_EXT_read_format_bgra extension").
    // GL_RGBA read-back is mandatory and always available. The bytes then
    // land as R,G,B,X; the WebP encoder wants B,G,R,X (WebPEncodeBGRA), so
    // we swap R<->B in place below.
    if (!wlr_renderer_begin_with_buffer(r, render_buf)) return false;
    bool ok = wlr_renderer_read_pixels(
        r, DRM_FORMAT_XBGR8888,
        (uint32_t)stride_, (uint32_t)w, (uint32_t)h,
        /*src_x*/ 0, /*src_y*/ 0, /*dst_x*/ 0, /*dst_y*/ 0,
        buf_.data());
    wlr_renderer_end(r);
    if (!ok) return false;

    // RGBX -> BGRX: swap the R and B channels for the BGRA encoder.
    for (int y = 0; y < h; y++) {
        uint8_t* row = buf_.data() + (size_t)y * stride_;
        for (int x = 0; x < w; x++) {
            uint8_t* px = row + (size_t)x * 4;
            uint8_t t = px[0];
            px[0] = px[2];
            px[2] = t;
        }
    }

    w_ = w;
    h_ = h;

    // Damage tracking: encode/send only the changed sub-rect. On a full
    // capture (first frame or resize) the whole surface is dirty. Else use
    // the surface's effective damage (surface-local; matches buffer coords
    // at scale 1) bounding box, clamped to the surface. Empty damage with
    // no size change means nothing visibly changed → skip the frame
    // (return false) so we don't re-encode/transmit an identical image.
    if (full_capture) {
        dirty_x = 0;
        dirty_y = 0;
        dirty_w = w;
        dirty_h = h;
        return true;
    }

    pixman_region32_t damage;
    pixman_region32_init(&damage);
    wlr_surface_get_effective_damage(surface, &damage);
    const pixman_box32_t* ext = pixman_region32_extents(&damage);
    // Damage is surface-local (full buffer); shift into crop-local coords
    // (subtract the crop origin) and clamp to the cropped frame.
    int x0 = ext->x1 - src_x; if (x0 < 0) x0 = 0;
    int y0 = ext->y1 - src_y; if (y0 < 0) y0 = 0;
    int x1 = ext->x2 - src_x; if (x1 > w) x1 = w;
    int y1 = ext->y2 - src_y; if (y1 > h) y1 = h;
    bool empty = !pixman_region32_not_empty(&damage) || x1 <= x0 || y1 <= y0;
    pixman_region32_fini(&damage);

    if (empty) {
        return false; // nothing changed; caller skips this frame
    }
    dirty_x = x0;
    dirty_y = y0;
    dirty_w = x1 - x0;
    dirty_h = y1 - y0;
    return true;
}

} // namespace wash

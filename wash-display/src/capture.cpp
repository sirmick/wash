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

bool SurfaceCapture::capture(struct wlr_surface* surface, struct wlr_renderer* renderer) {
    if (!surface || !renderer) return false;

    struct wlr_texture* texture = wlr_surface_get_texture(surface);
    if (!texture) return false;

    int w = (int)texture->width;
    int h = (int)texture->height;
    if (w <= 0 || h <= 0) return false;

    // The texture's own renderer is the one that can read it back.
    struct wlr_renderer* r = texture->renderer ? texture->renderer : renderer;

    struct wlr_buffer* render_buf = (struct wlr_buffer*)render_buf_;

    // A size change (or first capture) forces a full-frame dirty rect —
    // the FE canvas is resized and must be fully repainted.
    bool full_capture = (!render_buf || rb_w_ != w || rb_h_ != h);

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

    // Draw the client texture into the render target.
    struct wlr_buffer_pass_options pass_opts;
    std::memset(&pass_opts, 0, sizeof pass_opts);
    struct wlr_render_pass* pass = wlr_renderer_begin_buffer_pass(r, render_buf, &pass_opts);
    if (!pass) return false;

    struct wlr_render_texture_options tex_opts;
    std::memset(&tex_opts, 0, sizeof tex_opts);
    tex_opts.texture = texture;
    tex_opts.dst_box.x = 0;
    tex_opts.dst_box.y = 0;
    tex_opts.dst_box.width = w;
    tex_opts.dst_box.height = h;
    wlr_render_pass_add_texture(pass, &tex_opts);
    if (!wlr_render_pass_submit(pass)) return false;

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
    int x0 = ext->x1 < 0 ? 0 : ext->x1;
    int y0 = ext->y1 < 0 ? 0 : ext->y1;
    int x1 = ext->x2 > w ? w : ext->x2;
    int y1 = ext->y2 > h ? h : ext->y2;
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

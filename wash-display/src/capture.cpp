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
// Composite the surface tree (root + subsurfaces) into the render target AND
// accumulate the dirty rect. wlr_surface_for_each_surface walks the whole tree
// giving each surface's offset (sx,sy) in root-surface coords; we draw each
// textured surface at (sx,sy) minus the crop origin, so subsurface content
// (e.g. a browser's web-content surface) lands in the buffer. The render pass
// clips to the target, so the root's transparent CSD shadow margin (outside
// the crop) is dropped. Surfaces without a texture are skipped.
//
// Damage: a surface contributes to the dirty rect only when its commit seq
// advances since we last saw it (`seq`), so a static layer's stale last-commit
// damage doesn't inflate every frame. A seq advance with empty effective
// damage falls back to the surface's full bounds (a buffer swap without an
// explicit damage request must still be redrawn). Position/size/order changes
// dirty both old and new bounds, and vanished surfaces dirty their old bounds
// so the browser clears stale pixels from its persistent canvas.
struct CompositeCtx {
    struct wlr_render_pass* pass;
    int off_x;   // crop origin x in root-surface coords
    int off_y;   // crop origin y
    int buf_w;   // target bounds (for clamping damage)
    int buf_h;
    int drawn;   // count of textured surfaces composited
    int order;   // textured-surface paint order in this tree walk
    std::map<struct wlr_surface*, SurfaceCaptureState>* prev; // last frame's state (read)
    std::map<struct wlr_surface*, SurfaceCaptureState>* next; // this frame's state (write)
    bool have_dmg;         // accumulator below is valid
    int dx0, dy0, dx1, dy1; // dirty bbox in buffer coords
};

void add_dirty(CompositeCtx* c, int x0, int y0, int x1, int y1) {
    if (x0 < 0) x0 = 0;
    if (y0 < 0) y0 = 0;
    if (x1 > c->buf_w) x1 = c->buf_w;
    if (y1 > c->buf_h) y1 = c->buf_h;
    if (x1 <= x0 || y1 <= y0) return;
    if (!c->have_dmg) {
        c->have_dmg = true;
        c->dx0 = x0; c->dy0 = y0; c->dx1 = x1; c->dy1 = y1;
    } else {
        if (x0 < c->dx0) c->dx0 = x0;
        if (y0 < c->dy0) c->dy0 = y0;
        if (x1 > c->dx1) c->dx1 = x1;
        if (y1 > c->dy1) c->dy1 = y1;
    }
}

void add_bounds(CompositeCtx* c, const SurfaceCaptureState& st) {
    add_dirty(c, st.x, st.y, st.x + st.w, st.y + st.h);
}

void composite_surface_cb(struct wlr_surface* s, int sx, int sy, void* data) {
    auto* c = static_cast<CompositeCtx*>(data);
    struct wlr_texture* tex = wlr_surface_get_texture(s);
    if (!tex) return;
    const int ox = sx - c->off_x, oy = sy - c->off_y;
    struct wlr_render_texture_options o;
    std::memset(&o, 0, sizeof o);
    o.texture = tex;
    o.dst_box.x = ox;
    o.dst_box.y = oy;
    o.dst_box.width = (int)tex->width;
    o.dst_box.height = (int)tex->height;
    // src-over blend (premultiplied, the default): a surface's transparent
    // pixels let the layer beneath show through. The target is cleared to
    // transparent before the walk, so the bottom surface blends onto nothing
    // (= verbatim) and upper subsurfaces composite correctly (M8c).
    o.blend_mode = WLR_RENDER_BLEND_MODE_PREMULTIPLIED;
    wlr_render_pass_add_texture(c->pass, &o);
    c->drawn++;

    // Did this surface change since last frame? Record into `next` regardless
    // (the caller swaps next→prev, which prunes surfaces no longer in the tree
    // and so is safe against surface-pointer reuse).
    const uint32_t cur = s->current.seq;
    SurfaceCaptureState now;
    now.seq = cur;
    now.texture = reinterpret_cast<uintptr_t>(tex);
    now.x = ox;
    now.y = oy;
    now.w = (int)tex->width;
    now.h = (int)tex->height;
    now.order = c->order++;
    (*c->next)[s] = now;
    auto it = c->prev->find(s);
    if (it == c->prev->end()) {
        add_bounds(c, now);
        return;
    }

    const SurfaceCaptureState& old = it->second;
    const bool seq_changed = old.seq != cur;
    const bool multi_commit = seq_changed && (uint32_t)(cur - old.seq) != 1;
    const bool layout_changed =
        old.texture != now.texture || old.x != now.x || old.y != now.y ||
        old.w != now.w || old.h != now.h || old.order != now.order;

    if (!seq_changed && !layout_changed) return;

    // Position/size/order/texture changes need both old and new bounds: the
    // new pixels must be sent, and old pixels must be cleared/revealed.
    // If the output frame skipped over multiple client commits, effective
    // damage only describes the latest one, so fall back to the full current
    // bounds to avoid dropping earlier partial updates.
    if (layout_changed || multi_commit) {
        if (layout_changed) add_bounds(c, old);
        add_bounds(c, now);
        return;
    }

    // Its dirty region (surface-local) → buffer coords. Fall back to the
    // surface's full bounds when the client committed without explicit damage.
    int x0, y0, x1, y1;
    pixman_region32_t dmg;
    pixman_region32_init(&dmg);
    wlr_surface_get_effective_damage(s, &dmg);
    if (pixman_region32_not_empty(&dmg)) {
        const pixman_box32_t* e = pixman_region32_extents(&dmg);
        x0 = e->x1 + ox; y0 = e->y1 + oy; x1 = e->x2 + ox; y1 = e->y2 + oy;
    } else {
        x0 = ox; y0 = oy; x1 = ox + (int)tex->width; y1 = oy + (int)tex->height;
    }
    pixman_region32_fini(&dmg);

    add_dirty(c, x0, y0, x1, y1);
}
} // namespace

bool SurfaceCapture::capture(struct wlr_surface* surface, struct wlr_renderer* renderer,
                             int crop_x, int crop_y, int crop_w, int crop_h,
                             bool force_full, bool preserve_alpha) {
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
        // ARGB (alpha-capable), not XRGB: popups (menus) are shaped — rounded
        // corners + drop shadow are transparent in the client buffer, and we
        // must preserve that alpha or it flattens to black (M8c). Opaque app
        // content has alpha=255 and is unaffected.
        fmt.format = DRM_FORMAT_ARGB8888;
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

    // Clear the (reused) target to transparent first, then composite the tree
    // with src-over blending below — so a subsurface's transparent margin
    // (e.g. a browser's omnibox-dropdown overlay) shows the layer BENEATH it
    // instead of overwriting it. The whole tree is redrawn every frame, so a
    // full clear is correct (damage only limits read-back/encode, not this).
    struct wlr_render_rect_options clear;
    std::memset(&clear, 0, sizeof clear);
    clear.box = { 0, 0, w, h };
    clear.color = { 0.f, 0.f, 0.f, 0.f };
    clear.blend_mode = WLR_RENDER_BLEND_MODE_NONE;
    wlr_render_pass_add_rect(pass, &clear);

    std::map<struct wlr_surface*, SurfaceCaptureState> next;
    CompositeCtx ctx{ pass, src_x, src_y, w, h, 0, 0, &states_, &next, false, 0, 0, 0, 0 };
    wlr_surface_for_each_surface(surface, composite_surface_cb, &ctx);
    if (!wlr_render_pass_submit(pass)) return false;
    if (ctx.drawn == 0) return false; // nothing textured yet

    // Surfaces present last frame but absent now need their previous bounds
    // resent after the clear+composite pass, otherwise stale pixels remain on
    // the FE's persistent canvas.
    for (const auto& [surf, st] : states_) {
        if (next.find(surf) == next.end()) add_bounds(&ctx, st);
    }

    // Decide the dirty rect from the tree walk. A resize/first-frame, a
    // forced full capture means the whole window; otherwise the union of the
    // surfaces that actually changed, moved, appeared, or vanished. If
    // something committed but nothing visibly changed, skip the frame.
    if (full_capture) {
        dirty_x = 0; dirty_y = 0; dirty_w = w; dirty_h = h;
    } else if (ctx.have_dmg) {
        dirty_x = ctx.dx0; dirty_y = ctx.dy0;
        dirty_w = ctx.dx1 - ctx.dx0; dirty_h = ctx.dy1 - ctx.dy0;
    } else {
        return false;
    }

    // Grow-only CPU buffer (no per-frame alloc) and read the pixels back.
    stride_ = w * 4;
    size_t need = (size_t)stride_ * (size_t)h;
    if (buf_.size() < need) buf_.resize(need);

    // Read back only the dirty sub-rect (src=dst=dirty origin, full stride →
    // the pixels land at their real position in buf_; the rest of buf_ keeps
    // last frame's bytes, which the encoder doesn't read). On a full frame the
    // rect is the whole window, so this is the previous behaviour.
    // Format note: ABGR8888 (-> GL_RGBA), NOT ARGB8888 (-> GL_BGRA_EXT): the
    // latter needs GL_EXT_read_format_bgra, which Mesa's surfaceless/llvmpipe
    // GLES2 context does not advertise, so glReadPixels would fail. GL_RGBA is
    // mandatory and always available; bytes land R,G,B,A and the WebP encoder
    // wants B,G,R,A (WebPEncodeBGRA), so we swap R<->B below (alpha kept).
    if (!wlr_renderer_begin_with_buffer(r, render_buf)) return false;
    bool ok = wlr_renderer_read_pixels(
        r, DRM_FORMAT_ABGR8888,
        (uint32_t)stride_, (uint32_t)dirty_w, (uint32_t)dirty_h,
        /*src_x*/ dirty_x, /*src_y*/ dirty_y, /*dst_x*/ dirty_x, /*dst_y*/ dirty_y,
        buf_.data());
    wlr_renderer_end(r);
    if (!ok) return false;

    // RGBA -> BGRA for the encoder (swap R<->B), over the dirty rect only, and
    // UN-PREMULTIPLY: Wayland client buffers carry premultiplied alpha, but
    // WebP (WebPPictureImportBGRA) wants straight alpha — without this, a soft
    // drop-shadow's mid-alpha pixels stay multiplied and read too dark (the
    // "menu looked wrong" halo, M8c). Opaque (a=255) and clear (a=0) pixels are
    // untouched; only 0<a<255 is divided back out.
    for (int y = dirty_y; y < dirty_y + dirty_h; y++) {
        uint8_t* row = buf_.data() + (size_t)y * stride_;
        for (int x = dirty_x; x < dirty_x + dirty_w; x++) {
            uint8_t* px = row + (size_t)x * 4;
            uint8_t t = px[0];
            px[0] = px[2];
            px[2] = t;
            if (!preserve_alpha) {
                px[3] = 255; // opaque window: ignore stray client alpha so the
                             // desktop/other windows never show through chrome
            } else {
                uint8_t a = px[3];
                if (a != 0 && a != 255) {
                    int b = px[0] * 255 / a, g = px[1] * 255 / a, rr = px[2] * 255 / a;
                    px[0] = b > 255 ? 255 : (uint8_t)b;
                    px[1] = g > 255 ? 255 : (uint8_t)g;
                    px[2] = rr > 255 ? 255 : (uint8_t)rr;
                }
            }
        }
    }

    // Adopt this frame's seqs ONLY now that the read-back succeeded — if it
    // had failed above we keep the old seqs so the changed surfaces are
    // retried next frame instead of being treated as already-encoded (which
    // would drop their update and leave stale pixels). Prunes vanished surfaces.
    states_.swap(next);

    w_ = w;
    h_ = h;
    return true;
}

SurfaceCapture::~SurfaceCapture() {
    // Drop the pooled GPU render target (allocated lazily, reused across
    // frames, previously freed only on resize) so closing a window doesn't
    // leak its wlr_buffer.
    if (render_buf_) wlr_buffer_drop((struct wlr_buffer*)render_buf_);
}

} // namespace wash

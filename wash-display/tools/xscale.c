// xscale — a deliberately busy X11 test client for the wash-display
// compositor. It makes resize / scaling / offset / clipping bugs VISUALLY
// obvious and logs every size + input event, so we can shake out the
// display pipeline (geometry feedback, damage, input injection, keyboard).
//
// Build:  cc -O2 -o xscale xscale.c -lX11
// Run:    DISPLAY=:N ./xscale        (or type the full path in a wash term)
//
// What it draws — everything in DEVICE PIXELS, origin top-left:
//   * a 1px outer border flush to each window edge. If a side is missing,
//     that edge is clipped or the surface size != the frame.
//   * ruler ticks on the top + left edges: minor every 10px, major every
//     50px, numeric labels every 100px.
//   * a 50px grid.
//   * BOTH diagonals corner-to-corner. Non-uniform scaling skews them, so
//     a clean symmetric "X" meeting exactly at center == square 1:1 pixels.
//   * a center crosshair + a big "W x H" readout (the size the GUEST
//     believes it is — compare against the wash frame).
//   * corner coordinate labels: "0,0" (top-left) and "W-1 x H-1" (bottom-
//     right, inset). If you can't read the bottom-right label, the right/
//     bottom edge is clipped or the guest is larger than the frame.
//   * a RED ring + label at the last pointer position the X server reported
//     — i.e. where wash thinks your cursor is. Click/move and see if it
//     tracks your real cursor (input-mapping / scaling check).
//   * the last key (keysym + text) printed near the pointer.
//
// Logging (stderr, +ms since start):
//   ConfigureNotify (new vs previous size), Expose, Map, key press/release
//   (keycode, keysym name, lookup text), button press/release, motion
//   (throttled), enter/leave, focus in/out, mapping changes.
//
// Closes cleanly on WM_DELETE_WINDOW (exercises the polite-close path that
// plain xclock lacks) and on 'q' / Escape. Uses a backing Pixmap so the
// frequent redraws on motion don't flicker.

#include <X11/Xlib.h>
#include <X11/Xutil.h>
#include <X11/keysym.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

static long t0_ms;
static long now_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (long)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}
static void lg(const char *fmt, ...) {
    va_list ap;
    va_start(ap, fmt);
    fprintf(stderr, "[+%6ldms] ", now_ms() - t0_ms);
    vfprintf(stderr, fmt, ap);
    fputc('\n', stderr);
    fflush(stderr);
    va_end(ap);
}

static Display *dpy;
static int scr;
static Window win;
static GC gc;
static Pixmap buf;
static XFontStruct *font;
static int W = 480, H = 320;
static int px = -1, py = -1;        // last reported pointer position
static char lastkey[96] = "(none)";
static unsigned long c_bg, c_fg, c_grid, c_major, c_diag, c_ptr;

static unsigned long named(const char *name, unsigned long fallback) {
    XColor c, exact;
    Colormap cm = DefaultColormap(dpy, scr);
    if (XAllocNamedColor(dpy, cm, name, &c, &exact)) return c.pixel;
    return fallback;
}

static void make_buffer(void) {
    if (buf) XFreePixmap(dpy, buf);
    buf = XCreatePixmap(dpy, win, (unsigned)W, (unsigned)H,
                        (unsigned)DefaultDepth(dpy, scr));
}

static void redraw(void) {
    char s[128];

    // background
    XSetForeground(dpy, gc, c_bg);
    XFillRectangle(dpy, buf, gc, 0, 0, (unsigned)W, (unsigned)H);

    // 50px grid
    XSetForeground(dpy, gc, c_grid);
    for (int x = 0; x <= W; x += 50) XDrawLine(dpy, buf, gc, x, 0, x, H);
    for (int y = 0; y <= H; y += 50) XDrawLine(dpy, buf, gc, 0, y, W, y);

    // diagonals (skew => non-uniform scale)
    XSetForeground(dpy, gc, c_diag);
    XDrawLine(dpy, buf, gc, 0, 0, W - 1, H - 1);
    XDrawLine(dpy, buf, gc, W - 1, 0, 0, H - 1);

    // ruler ticks: top + left. minor 10, major 50, labels 100.
    XSetForeground(dpy, gc, c_major);
    for (int x = 0; x <= W; x += 10) {
        int len = (x % 50 == 0) ? 12 : 6;
        XDrawLine(dpy, buf, gc, x, 0, x, len);
        if (x % 100 == 0) {
            snprintf(s, sizeof s, "%d", x);
            XDrawString(dpy, buf, gc, x + 2, 24, s, (int)strlen(s));
        }
    }
    for (int y = 0; y <= H; y += 10) {
        int len = (y % 50 == 0) ? 12 : 6;
        XDrawLine(dpy, buf, gc, 0, y, len, y);
        if (y % 100 == 0) {
            snprintf(s, sizeof s, "%d", y);
            XDrawString(dpy, buf, gc, 14, y + 12, s, (int)strlen(s));
        }
    }

    // outer 1px border flush to the edges
    XSetForeground(dpy, gc, c_fg);
    XDrawRectangle(dpy, buf, gc, 0, 0, (unsigned)(W - 1), (unsigned)(H - 1));

    // center crosshair + big size readout
    int cx = W / 2, cy = H / 2;
    XDrawLine(dpy, buf, gc, cx - 12, cy, cx + 12, cy);
    XDrawLine(dpy, buf, gc, cx, cy - 12, cx, cy + 12);
    snprintf(s, sizeof s, "%d x %d", W, H);
    int tw = font ? XTextWidth(font, s, (int)strlen(s)) : (int)strlen(s) * 6;
    XDrawString(dpy, buf, gc, cx - tw / 2, cy - 18, s, (int)strlen(s));

    // corner coordinate labels (inset so they're readable)
    XDrawString(dpy, buf, gc, 16, 40, "0,0", 3);
    snprintf(s, sizeof s, "%d x %d (BR)", W - 1, H - 1);
    tw = font ? XTextWidth(font, s, (int)strlen(s)) : (int)strlen(s) * 6;
    XDrawString(dpy, buf, gc, W - tw - 4, H - 6, s, (int)strlen(s));

    // last key, top-center
    snprintf(s, sizeof s, "key: %s", lastkey);
    XDrawString(dpy, buf, gc, cx - 80, cy + 22, s, (int)strlen(s));

    // pointer ring where the X server thinks the cursor is
    if (px >= 0) {
        XSetForeground(dpy, gc, c_ptr);
        XDrawArc(dpy, buf, gc, px - 10, py - 10, 20, 20, 0, 360 * 64);
        XDrawLine(dpy, buf, gc, px - 14, py, px + 14, py);
        XDrawLine(dpy, buf, gc, px, py - 14, px, py + 14);
        snprintf(s, sizeof s, "%d,%d", px, py);
        XDrawString(dpy, buf, gc, px + 12, py - 12, s, (int)strlen(s));
    }

    XCopyArea(dpy, buf, win, gc, 0, 0, (unsigned)W, (unsigned)H, 0, 0);
    XFlush(dpy);
}

int main(void) {
    t0_ms = now_ms();
    dpy = XOpenDisplay(NULL);
    if (!dpy) {
        fprintf(stderr, "xscale: cannot open display '%s'\n",
                getenv("DISPLAY") ? getenv("DISPLAY") : "(unset)");
        return 1;
    }
    scr = DefaultScreen(dpy);
    lg("xscale start: display=%s depth=%d screen=%dx%d",
       getenv("DISPLAY") ? getenv("DISPLAY") : "?", DefaultDepth(dpy, scr),
       DisplayWidth(dpy, scr), DisplayHeight(dpy, scr));

    c_bg    = named("#101418", BlackPixel(dpy, scr));
    c_fg    = named("#e8e8e8", WhitePixel(dpy, scr));
    c_grid  = named("#2a3540", c_fg);
    c_major = named("#7fc7ff", c_fg);
    c_diag  = named("#ff6f5e", c_fg);
    c_ptr   = named("#5dff8a", c_fg);

    win = XCreateSimpleWindow(dpy, RootWindow(dpy, scr), 0, 0,
                              (unsigned)W, (unsigned)H, 0,
                              c_fg, c_bg);
    XStoreName(dpy, win, "xscale — wash display test");

    // size hints: allow free resize, but advertise a base + min so we can
    // see whether the compositor honours them.
    XSizeHints *sh = XAllocSizeHints();
    sh->flags = PMinSize | PBaseSize;
    sh->min_width = 120; sh->min_height = 90;
    sh->base_width = W; sh->base_height = H;
    XSetWMNormalHints(dpy, win, sh);
    XFree(sh);

    Atom wm_delete = XInternAtom(dpy, "WM_DELETE_WINDOW", False);
    XSetWMProtocols(dpy, win, &wm_delete, 1);

    XSelectInput(dpy, win,
                 ExposureMask | StructureNotifyMask | KeyPressMask |
                 KeyReleaseMask | ButtonPressMask | ButtonReleaseMask |
                 PointerMotionMask | EnterWindowMask | LeaveWindowMask |
                 FocusChangeMask);

    gc = XCreateGC(dpy, win, 0, NULL);
    font = XLoadQueryFont(dpy, "9x15");
    if (!font) font = XLoadQueryFont(dpy, "fixed");
    if (font) XSetFont(dpy, gc, font->fid);

    make_buffer();
    XMapWindow(dpy, win);

    long last_motion_log = 0;
    for (;;) {
        XEvent ev;
        XNextEvent(dpy, &ev);
        switch (ev.type) {
        case MapNotify:
            lg("MapNotify");
            break;
        case ConfigureNotify: {
            int nw = ev.xconfigure.width, nh = ev.xconfigure.height;
            if (nw != W || nh != H) {
                lg("ConfigureNotify: %dx%d -> %dx%d  (x=%d y=%d)",
                   W, H, nw, nh, ev.xconfigure.x, ev.xconfigure.y);
                W = nw; H = nh;
                make_buffer();
                redraw();
            }
            break;
        }
        case Expose:
            if (ev.xexpose.count == 0) {
                lg("Expose %dx%d+%d+%d", ev.xexpose.width, ev.xexpose.height,
                   ev.xexpose.x, ev.xexpose.y);
                redraw();
            }
            break;
        case KeyPress:
        case KeyRelease: {
            char txt[32];
            KeySym ks;
            int n = XLookupString(&ev.xkey, txt, sizeof txt - 1, &ks, NULL);
            txt[n > 0 ? n : 0] = '\0';
            const char *ksn = XKeysymToString(ks);
            lg("%s keycode=%u keysym=%s(0x%lx) text='%s' state=0x%x x=%d y=%d",
               ev.type == KeyPress ? "KeyPress  " : "KeyRelease",
               ev.xkey.keycode, ksn ? ksn : "?", (unsigned long)ks,
               txt, ev.xkey.state, ev.xkey.x, ev.xkey.y);
            if (ev.type == KeyPress) {
                snprintf(lastkey, sizeof lastkey, "%s '%s'", ksn ? ksn : "?", txt);
                redraw();
                if (ks == XK_q || ks == XK_Escape) {
                    lg("quit (q/Esc)");
                    goto done;
                }
            }
            break;
        }
        case ButtonPress:
        case ButtonRelease:
            lg("%s button=%u x=%d y=%d state=0x%x",
               ev.type == ButtonPress ? "ButtonPress  " : "ButtonRelease",
               ev.xbutton.button, ev.xbutton.x, ev.xbutton.y, ev.xbutton.state);
            px = ev.xbutton.x; py = ev.xbutton.y;
            redraw();
            break;
        case MotionNotify:
            px = ev.xmotion.x; py = ev.xmotion.y;
            if (now_ms() - last_motion_log >= 100) {
                lg("Motion x=%d y=%d state=0x%x", px, py, ev.xmotion.state);
                last_motion_log = now_ms();
            }
            redraw();
            break;
        case EnterNotify:
            lg("EnterNotify x=%d y=%d", ev.xcrossing.x, ev.xcrossing.y);
            break;
        case LeaveNotify:
            lg("LeaveNotify x=%d y=%d", ev.xcrossing.x, ev.xcrossing.y);
            px = py = -1;
            redraw();
            break;
        case FocusIn:
            lg("FocusIn");
            break;
        case FocusOut:
            lg("FocusOut");
            break;
        case MappingNotify:
            XRefreshKeyboardMapping(&ev.xmapping);
            lg("MappingNotify");
            break;
        case ClientMessage:
            if ((Atom)ev.xclient.data.l[0] == wm_delete) {
                lg("WM_DELETE_WINDOW — closing politely");
                goto done;
            }
            break;
        }
    }
done:
    if (font) XFreeFont(dpy, font);
    if (buf) XFreePixmap(dpy, buf);
    XFreeGC(dpy, gc);
    XDestroyWindow(dpy, win);
    XCloseDisplay(dpy);
    lg("xscale exit");
    return 0;
}

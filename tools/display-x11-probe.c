// display-x11-probe — a dependency-free xcb client that exercises the X11
// paths the GTK/Qt guests can't reach on purpose (e2e/tests/display-x11-probe.spec.ts):
//
//   or-untyped   map a normal toplevel, then an override-redirect window with
//                NO _NET_WM_WINDOW_TYPE (a Steam toast / Wine helper / Java
//                popup shape). The compositor must overlay it WITHOUT taking a
//                pointer grab on the parent (log: "X11 popup mapped ... grab=0").
//   or-menu      same, typed _NET_WM_WINDOW_TYPE_MENU + WM_TRANSIENT_FOR =
//                the toplevel → a real menu, grab=1, parented via transient-for.
//   fullscreen   map a toplevel, then ask for _NET_WM_STATE_FULLSCREEN via the
//                EWMH client message. The compositor must answer with a
//                ConfigureNotify to the screen size (logged here as
//                "XPROBE: configure WxH") — an unhandled request leaves the
//                client believing it is fullscreen at its old size (mpv/SDL).
//
// Every event of interest is appended to the log file (argv[2]) as one
// "XPROBE: ..." line so the spec can assert on it. Exits on its own after
// ~20s (alarm) so a test never leaks it. Build: cc -o probe display-x11-probe.c -lxcb
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <xcb/xcb.h>

static FILE* g_log;

static void logf_(const char* fmt, const char* a, int b, int c) {
    if (!g_log) return;
    fprintf(g_log, fmt, a, b, c);
    fputc('\n', g_log);
    fflush(g_log);
}

static xcb_atom_t atom(xcb_connection_t* c, const char* name) {
    xcb_intern_atom_reply_t* r =
        xcb_intern_atom_reply(c, xcb_intern_atom(c, 0, (uint16_t)strlen(name), name), NULL);
    xcb_atom_t a = r ? r->atom : XCB_ATOM_NONE;
    free(r);
    return a;
}

int main(int argc, char** argv) {
    if (argc < 2) {
        fprintf(stderr, "usage: %s or-untyped|or-menu|fullscreen [logfile]\n", argv[0]);
        return 2;
    }
    const char* mode = argv[1];
    g_log = argc >= 3 ? fopen(argv[2], "a") : stderr;
    alarm(20);

    xcb_connection_t* c = xcb_connect(NULL, NULL);
    if (!c || xcb_connection_has_error(c)) {
        logf_("XPROBE: connect failed%s%d%d", "", 0, 0);
        return 1;
    }
    xcb_screen_t* screen = xcb_setup_roots_iterator(xcb_get_setup(c)).data;

    // A normal toplevel: the parent every override-redirect window belongs to.
    xcb_window_t top = xcb_generate_id(c);
    uint32_t mask = XCB_CW_BACK_PIXEL | XCB_CW_EVENT_MASK;
    uint32_t vals[] = {screen->white_pixel, XCB_EVENT_MASK_STRUCTURE_NOTIFY | XCB_EVENT_MASK_EXPOSURE};
    xcb_create_window(c, XCB_COPY_FROM_PARENT, top, screen->root, 0, 0, 240, 160, 0,
                      XCB_WINDOW_CLASS_INPUT_OUTPUT, screen->root_visual, mask, vals);
    const char* title = "xprobe";
    xcb_change_property(c, XCB_PROP_MODE_REPLACE, top, XCB_ATOM_WM_NAME, XCB_ATOM_STRING, 8,
                        (uint32_t)strlen(title), title);
    xcb_map_window(c, top);
    xcb_flush(c);

    int top_mapped = 0, or_done = 0;
    xcb_generic_event_t* ev;
    while ((ev = xcb_wait_for_event(c))) {
        uint8_t t = ev->response_type & ~0x80;
        if (t == XCB_MAP_NOTIFY && ((xcb_map_notify_event_t*)ev)->window == top && !top_mapped) {
            top_mapped = 1;
            logf_("XPROBE: mapped%s%d%d", "", 0, 0);
            if (!strcmp(mode, "or-untyped") || !strcmp(mode, "or-menu")) {
                xcb_window_t orw = xcb_generate_id(c);
                uint32_t omask = XCB_CW_BACK_PIXEL | XCB_CW_OVERRIDE_REDIRECT | XCB_CW_EVENT_MASK;
                uint32_t ovals[] = {screen->black_pixel, 1, XCB_EVENT_MASK_STRUCTURE_NOTIFY};
                xcb_create_window(c, XCB_COPY_FROM_PARENT, orw, screen->root, 40, 50, 120, 80, 0,
                                  XCB_WINDOW_CLASS_INPUT_OUTPUT, screen->root_visual, omask, ovals);
                if (!strcmp(mode, "or-menu")) {
                    xcb_atom_t wt = atom(c, "_NET_WM_WINDOW_TYPE");
                    xcb_atom_t menu = atom(c, "_NET_WM_WINDOW_TYPE_MENU");
                    xcb_change_property(c, XCB_PROP_MODE_REPLACE, orw, wt, XCB_ATOM_ATOM, 32, 1, &menu);
                    xcb_change_property(c, XCB_PROP_MODE_REPLACE, orw, XCB_ATOM_WM_TRANSIENT_FOR,
                                        XCB_ATOM_WINDOW, 32, 1, &top);
                }
                xcb_map_window(c, orw);
                xcb_flush(c);
                logf_("XPROBE: or %s mapped%d%d", mode, 0, 0);
                or_done = 1;
            } else if (!strcmp(mode, "fullscreen")) {
                xcb_client_message_event_t m;
                memset(&m, 0, sizeof m);
                m.response_type = XCB_CLIENT_MESSAGE;
                m.format = 32;
                m.window = top;
                m.type = atom(c, "_NET_WM_STATE");
                m.data.data32[0] = 1; // _NET_WM_STATE_ADD
                m.data.data32[1] = atom(c, "_NET_WM_STATE_FULLSCREEN");
                m.data.data32[2] = 0;
                m.data.data32[3] = 1; // source: application
                xcb_send_event(c, 0, screen->root,
                               XCB_EVENT_MASK_SUBSTRUCTURE_REDIRECT | XCB_EVENT_MASK_SUBSTRUCTURE_NOTIFY,
                               (const char*)&m);
                xcb_flush(c);
                logf_("XPROBE: requested fullscreen%s%d%d", "", 0, 0);
            }
        } else if (t == XCB_CONFIGURE_NOTIFY) {
            xcb_configure_notify_event_t* ce = (xcb_configure_notify_event_t*)ev;
            if (ce->window == top) logf_("XPROBE: configure %s%dx%d", "", ce->width, ce->height);
        }
        free(ev);
        (void)or_done;
    }
    xcb_disconnect(c);
    return 0;
}

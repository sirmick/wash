#!/usr/bin/env python3
# wash-display test guest — a tiny, dependency-free (PyGObject is
# preinstalled) GTK3 app that exercises the interactive features the
# compositor must support, for both manual debugging and e2e regression:
#
#   - INPUT: a status label echoes the last click/key/scroll, so you can
#     SEE input land (manual) and the app prints each event to stdout/log
#     (automated).
#   - POPUPS: right-click (or the "Menu" button) opens a real context menu.
#     On Wayland that's an xdg_popup (M3a); on X11/Xwayland it's an
#     override-redirect window (M3b). Selecting an item prints it.
#   - CLIPBOARD: "Copy" puts a sentinel on the CLIPBOARD selection
#     (guest→wash bridge); "Paste" reads it back (wash→guest bridge) and
#     prints what it got.
#
# Backend is chosen by GDK_BACKEND (wayland | x11) so ONE script covers
# both popup/clipboard paths. Events are written to $WASH_TESTGUEST_LOG if
# set (so e2e can poll a file regardless of where stdout goes), and always
# echoed to stdout.
#
# Launch (from a wash terminal, env already carries DISPLAY/WAYLAND_DISPLAY):
#   GDK_BACKEND=wayland python3 tools/display-testguest.py
#   GDK_BACKEND=x11      python3 tools/display-testguest.py

import os
import sys

import gi

# Pin BOTH to 3.0 before importing — GTK4 is also installed, and importing
# Gdk without a version pulls 4.0, which then conflicts with Gtk 3.0.
gi.require_version("Gtk", "3.0")
gi.require_version("Gdk", "3.0")
from gi.repository import Gdk, Gtk  # noqa: E402

SENTINEL = "wash-clip-sentinel-42"

_logf = None
_logpath = os.environ.get("WASH_TESTGUEST_LOG")
if _logpath:
    _logf = open(_logpath, "a", buffering=1)


def log(msg):
    line = "GUEST: " + msg
    print(line, flush=True)
    if _logf:
        _logf.write(line + "\n")


class TestGuest(Gtk.Window):
    def __init__(self):
        super().__init__(title="wash display test")
        self.set_default_size(420, 300)

        box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=8)
        box.set_margin_top(12)
        box.set_margin_bottom(12)
        box.set_margin_start(12)
        box.set_margin_end(12)
        self.add(box)

        self.status = Gtk.Label(label="ready — click / type / right-click")
        self.status.set_xalign(0.0)
        box.pack_start(self.status, False, False, 0)

        self.entry = Gtk.Entry()
        self.entry.set_text(SENTINEL)
        box.pack_start(self.entry, False, False, 0)

        row = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        box.pack_start(row, False, False, 0)

        copy_btn = Gtk.Button(label="Copy")
        copy_btn.connect("clicked", self.on_copy)
        row.pack_start(copy_btn, False, False, 0)

        paste_btn = Gtk.Button(label="Paste")
        paste_btn.connect("clicked", self.on_paste)
        row.pack_start(paste_btn, False, False, 0)

        menu_btn = Gtk.Button(label="Menu")
        menu_btn.connect("clicked", lambda _b: self.popup_menu(None))
        row.pack_start(menu_btn, False, False, 0)

        # A drawing surface that reports clicks/keys (visible feedback +
        # the right-click menu trigger).
        self.area = Gtk.EventBox()
        self.area.set_above_child(True)
        filler = Gtk.Label(label="(right-click here for a menu)")
        self.area.add(filler)
        box.pack_start(self.area, True, True, 0)

        self.add_events(
            Gdk.EventMask.BUTTON_PRESS_MASK
            | Gdk.EventMask.KEY_PRESS_MASK
            | Gdk.EventMask.SCROLL_MASK
        )
        self.area.connect("button-press-event", self.on_button)
        self.connect("key-press-event", self.on_key)
        self.connect("scroll-event", self.on_scroll)
        self.connect("destroy", Gtk.main_quit)

        self.clipboard = Gtk.Clipboard.get(Gdk.SELECTION_CLIPBOARD)

    # --- input feedback ---
    def on_button(self, _w, ev):
        if ev.button == 3:  # right-click → context menu (popup)
            self.popup_menu(ev)
            return True
        self.set_status(f"click button={ev.button} x={int(ev.x)} y={int(ev.y)}")
        return False

    def on_key(self, _w, ev):
        name = Gdk.keyval_name(ev.keyval)
        # Keyboard triggers so an e2e can drive features position-
        # independently (no need to click specific buttons on the stream).
        if name in ("c", "C"):
            self.on_copy(None)
            return True
        if name in ("v", "V"):
            self.on_paste(None)
            return True
        if name in ("m", "M"):
            self.popup_menu(None)
            return True
        self.set_status(f"key {name}")
        return False

    def on_scroll(self, _w, ev):
        self.set_status(f"scroll dir={ev.direction.value_nick}")
        return False

    def set_status(self, msg):
        self.status.set_text(msg)
        log(msg)

    # --- popup menu ---
    def popup_menu(self, ev):
        menu = Gtk.Menu()
        for name in ("Alpha", "Beta", "Gamma"):
            item = Gtk.MenuItem(label=name)
            item.connect("activate", lambda _i, n=name: self.set_status(f"menu {n}"))
            menu.append(item)
        menu.show_all()
        log("menu opened")
        if ev is not None and hasattr(menu, "popup_at_pointer"):
            menu.popup_at_pointer(ev)
        else:
            menu.popup(None, None, None, None, 0, Gtk.get_current_event_time())

    # --- clipboard ---
    def on_copy(self, _b):
        text = self.entry.get_text()
        self.clipboard.set_text(text, -1)
        self.clipboard.store()
        self.set_status(f"copied {text!r}")

    def on_paste(self, _b):
        text = self.clipboard.wait_for_text()
        self.set_status(f"pasted {text!r}")


def main():
    backend = os.environ.get("GDK_BACKEND", "(default)")
    log(f"starting backend={backend}")
    win = TestGuest()
    win.show_all()
    log("mapped")
    Gtk.main()
    return 0


if __name__ == "__main__":
    sys.exit(main())

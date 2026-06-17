#!/usr/bin/env python3
# GTK4 sibling of display-testguest.py — exercises a GTK4 PopoverMenu
# (the popup structure gnome/libadwaita apps actually use) so we can tell
# whether clicking a GTK4 popover item lands, vs the GTK3 Gtk.Menu grab path.
# Activating an item logs "GUEST4: menu <name>" to $WASH_TESTGUEST_LOG.
import os
import sys

import gi

gi.require_version("Gtk", "4.0")
from gi.repository import Gio, Gtk  # noqa: E402

_logf = None
_logpath = os.environ.get("WASH_TESTGUEST_LOG")
if _logpath:
    _logf = open(_logpath, "a", buffering=1)


def log(msg):
    line = "GUEST4: " + msg
    print(line, flush=True)
    if _logf:
        _logf.write(line + "\n")


class App(Gtk.Application):
    def __init__(self):
        super().__init__(application_id="com.wash.testguest4")

    def do_activate(self):
        win = Gtk.ApplicationWindow(application=self)
        win.set_title("wash display test gtk4")
        win.set_default_size(420, 300)

        # Actions the menu items map to.
        for name in ("alpha", "beta", "gamma"):
            act = Gio.SimpleAction.new(name, None)
            act.connect("activate", lambda _a, _p, n=name: self.on_item(n))
            self.add_action(act)

        menu = Gio.Menu()
        menu.append("Alpha", "app.alpha")
        menu.append("Beta", "app.beta")
        menu.append("Gamma", "app.gamma")

        btn = Gtk.MenuButton(label="Menu")
        btn.set_menu_model(menu)  # GtkMenuButton → GtkPopoverMenu (xdg_popup)

        header = Gtk.HeaderBar()
        header.pack_start(btn)
        win.set_titlebar(header)

        box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=8)
        box.set_margin_top(12)
        box.set_margin_bottom(12)
        box.set_margin_start(12)
        box.set_margin_end(12)
        lbl = Gtk.Label(label="click Menu, then an item")
        box.append(lbl)
        win.set_child(box)

        win.present()
        log("mapped")

    def on_item(self, name):
        log(f"menu {name}")


def main():
    log("starting backend=" + os.environ.get("GDK_BACKEND", "(default)"))
    app = App()
    return app.run(None)


if __name__ == "__main__":
    sys.exit(main())

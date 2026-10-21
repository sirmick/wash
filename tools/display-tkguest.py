#!/usr/bin/env python3
# wash-display torture guest — Tk/X11 (needs only python3-tk, which the
# stock python3 ships with; no GTK/Qt/PyGObject). Tk has no Wayland backend,
# so this ALWAYS exercises the Xwayland bridge: X11 toplevels, override-
# redirect menus (Tk menus are OR toplevels with no WM_TRANSIENT_FOR — the
# compositor's fallback-parent path), X11 CLIPBOARD/PRIMARY, X key events.
#
# It is scriptable: a control fifo (--ctl) takes one command per line and
# every observable event is logged as a "GUEST: ..." line (stdout and
# $WASH_TESTGUEST_LOG / --log), so a harness drives it without touching the
# wash terminal and asserts on what the guest ACTUALLY received.
#
#   python3 tools/display-tkguest.py --ctl /tmp/g.fifo --log /tmp/g.log --title A
#
# Commands (one per line on the fifo; each acks with "GUEST: ack <cmd>"):
#   menu post            post the context menu at the pointer (OR window)
#   menu unpost          unpost it
#   menu toggle N DELAY  post/unpost N times, DELAY ms apart (menu storm)
#   or show W H          map an untyped override-redirect toplevel (no grab)
#   or hide              unmap it
#   clip set N           put N bytes of a deterministic pattern on CLIPBOARD
#   clip get             read CLIPBOARD, log its length + sha1
#   primary set N        same, PRIMARY selection
#   resize W H           ask the WM for a new size (wm geometry)
#   title T              set the window title
#   child N              open N extra toplevel windows
#   child close          close every extra toplevel
#   busy MS              block the Tk loop for MS (unresponsive-client probe)
#   paint N              repaint the canvas N times (damage storm)
#   quit                 exit 0
#
# Log lines (all prefixed "GUEST: "):
#   map/unmap/configure W H X Y (every ConfigureNotify), expose,
#   key down|up <keysym> mods=<shift,ctrl,alt,super> state=<hex>,
#   button down|up <n> X Y, motion X Y (throttled), enter/leave, focus in|out,
#   wheel <dir>, menu posted|unposted, menu pick <item>, clip ..., child ...
import argparse
import hashlib
import os
import sys
import threading
import time
import tkinter as tk

_t0 = time.monotonic()
_logf = None


def log(msg: str) -> None:
    line = "GUEST: +%06d %s" % (int((time.monotonic() - _t0) * 1000), msg)
    print(line, flush=True)
    if _logf:
        _logf.write(line + "\n")
        _logf.flush()


def mods(state: int) -> str:
    out = []
    if state & 0x1:
        out.append("shift")
    if state & 0x4:
        out.append("ctrl")
    if state & 0x8:
        out.append("alt")
    if state & 0x40:
        out.append("super")
    if state & 0x2:
        out.append("lock")
    return ",".join(out) or "-"


def pattern(n: int) -> str:
    # Deterministic, non-repeating-looking, pure ASCII: easy to verify by sha1.
    chunk = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-\n"
    s = (chunk * (n // len(chunk) + 1))[:n]
    return s


class Guest:
    def __init__(self, args):
        self.args = args
        self.root = tk.Tk()
        self.root.title(args.title)
        self.root.geometry("%dx%d" % (args.width, args.height))
        self.canvas = tk.Canvas(self.root, bg="#204060", highlightthickness=0)
        self.canvas.pack(fill="both", expand=True)
        self.status = tk.Label(self.root, text="wash tk guest", anchor="w", bg="#e0e0e0")
        self.status.pack(fill="x")
        self.children = []
        self.or_win = None
        self.paint_n = 0
        self.last_motion = 0.0
        self.last_size = (0, 0)

        self.menu = tk.Menu(self.root, tearoff=0)
        for item in ("Alpha", "Beta", "Gamma", "Delta"):
            self.menu.add_command(label=item, command=lambda i=item: self.pick(i))
        sub = tk.Menu(self.menu, tearoff=0)
        for item in ("Sub-1", "Sub-2"):
            sub.add_command(label=item, command=lambda i=item: self.pick(i))
        self.menu.add_cascade(label="More", menu=sub)
        self.menu.bind("<Map>", lambda e: log("menu posted"))
        self.menu.bind("<Unmap>", lambda e: log("menu unposted"))

        r = self.root
        r.bind("<KeyPress>", self.on_key_down)
        r.bind("<KeyRelease>", self.on_key_up)
        r.bind("<ButtonPress>", self.on_button_down)
        r.bind("<ButtonRelease>", self.on_button_up)
        r.bind("<MouseWheel>", lambda e: log("wheel %s delta=%d" % ("up" if e.delta > 0 else "down", e.delta)))
        r.bind("<Motion>", self.on_motion)
        r.bind("<Enter>", lambda e: log("enter"))
        r.bind("<Leave>", lambda e: log("leave"))
        r.bind("<FocusIn>", lambda e: log("focus in"))
        r.bind("<FocusOut>", lambda e: log("focus out"))
        r.bind("<Configure>", self.on_configure)
        r.bind("<Map>", lambda e: log("map"))
        r.bind("<Unmap>", lambda e: log("unmap"))
        r.bind("<Expose>", lambda e: log("expose"))
        r.bind("<Button-3>", self.on_right)
        r.protocol("WM_DELETE_WINDOW", self.on_close)
        self.paint()

    # --- events ---------------------------------------------------------
    def set_status(self, s):
        self.status.config(text=s)

    def on_key_down(self, e):
        log("key down %s mods=%s state=0x%x char=%r" % (e.keysym, mods(e.state), e.state, e.char))
        self.set_status("key %s" % e.keysym)

    def on_key_up(self, e):
        log("key up %s mods=%s state=0x%x" % (e.keysym, mods(e.state), e.state))

    def on_button_down(self, e):
        if e.num in (4, 5):
            log("wheel %s" % ("up" if e.num == 4 else "down"))
            return
        log("button down %d %d %d mods=%s" % (e.num, e.x_root - self.root.winfo_rootx(), e.y_root - self.root.winfo_rooty(), mods(e.state)))
        self.set_status("button %d @ %d,%d" % (e.num, e.x, e.y))

    def on_button_up(self, e):
        if e.num in (4, 5):
            return
        log("button up %d %d %d" % (e.num, e.x_root - self.root.winfo_rootx(), e.y_root - self.root.winfo_rooty()))

    def on_motion(self, e):
        now = time.monotonic()
        if now - self.last_motion > 0.1:
            self.last_motion = now
            log("motion %d %d" % (e.x_root - self.root.winfo_rootx(), e.y_root - self.root.winfo_rooty()))

    def on_configure(self, e):
        if e.widget is not self.root:
            return
        size = (e.width, e.height)
        if size != self.last_size:
            self.last_size = size
            log("configure %d %d %d %d" % (e.width, e.height, e.x, e.y))
            # The canvas widget gets its new geometry after this toplevel
            # Configure; paint once layout has settled so the testcard is drawn
            # at the NEW size (the harness samples quadrant colours).
            self.root.after_idle(self.paint)

    def on_right(self, e):
        self.post_menu(e.x_root, e.y_root)

    def on_close(self):
        log("close requested")
        self.root.destroy()

    def pick(self, item):
        log("menu pick %s" % item)
        self.set_status("picked %s" % item)

    # --- painting ---------------------------------------------------------
    def paint(self):
        c = self.canvas
        c.delete("all")
        w, h = max(1, c.winfo_width()), max(1, c.winfo_height())
        self.paint_n += 1
        c.create_rectangle(0, 0, w, h, fill="#204060", outline="")
        c.create_rectangle(0, 0, w // 2, h // 2, fill="#c03030", outline="")      # TL red
        c.create_rectangle(w // 2, 0, w, h // 2, fill="#30c030", outline="")      # TR green
        c.create_rectangle(0, h // 2, w // 2, h, fill="#3030c0", outline="")      # BL blue
        c.create_rectangle(w // 2, h // 2, w, h, fill="#f0f0f0", outline="")      # BR white
        c.create_line(0, 0, w, h, fill="black")
        c.create_line(0, h, w, 0, fill="black")
        c.create_text(w // 2, h // 2, text="%s %dx%d #%d" % (self.args.title, w, h, self.paint_n), fill="black", font=("Helvetica", 16))

    # --- commands ---------------------------------------------------------
    def post_menu(self, x=None, y=None):
        if x is None:
            x, y = self.root.winfo_pointerxy()
            if x < 0:
                x, y = self.root.winfo_rootx() + 40, self.root.winfo_rooty() + 40
        try:
            self.menu.tk_popup(x, y)
        finally:
            self.menu.grab_release()

    def unpost_menu(self):
        self.menu.unpost()

    def handle(self, line: str):
        parts = line.strip().split()
        if not parts:
            return
        cmd = parts[0]
        try:
            if cmd == "menu":
                if parts[1] == "post":
                    self.post_menu()
                elif parts[1] == "unpost":
                    self.unpost_menu()
                elif parts[1] == "toggle":
                    n, delay = int(parts[2]), int(parts[3])
                    self.menu_storm(n, delay)
            elif cmd == "or":
                if parts[1] == "show":
                    self.or_show(int(parts[2]), int(parts[3]))
                else:
                    self.or_hide()
            elif cmd == "clip":
                if parts[1] == "set":
                    s = pattern(int(parts[2]))
                    # Disown first: Tk keeps CLIPBOARD ownership across
                    # clipboard_clear/append, and without an owner CHANGE the
                    # compositor's xwm (XFixes selection notify) never learns
                    # the content changed. Real apps re-own per copy.
                    self.root.selection_clear(selection="CLIPBOARD")
                    self.root.update_idletasks()
                    self.root.clipboard_clear()
                    self.root.clipboard_append(s)
                    log("clip set %d sha1=%s" % (len(s), hashlib.sha1(s.encode()).hexdigest()))
                else:
                    try:
                        s = self.root.clipboard_get()
                        log("clip get %d sha1=%s" % (len(s), hashlib.sha1(s.encode()).hexdigest()))
                    except tk.TclError as ex:
                        log("clip get error %s" % ex)
            elif cmd == "primary":
                s = pattern(int(parts[2]))
                self.root.selection_handle(lambda off, maxc, s=s: s[int(off):int(off) + int(maxc)], selection="PRIMARY")
                self.root.selection_own(selection="PRIMARY")
                log("primary set %d" % len(s))
            elif cmd == "resize":
                self.root.geometry("%dx%d" % (int(parts[1]), int(parts[2])))
            elif cmd == "title":
                self.root.title(" ".join(parts[1:]))
                log("title set")
            elif cmd == "child":
                if parts[1] == "close":
                    for c in self.children:
                        c.destroy()
                    self.children = []
                    log("child closed all")
                else:
                    for i in range(int(parts[1])):
                        t = tk.Toplevel(self.root)
                        t.title("%s child %d" % (self.args.title, len(self.children) + 1))
                        t.geometry("200x120")
                        tk.Label(t, text="child %d" % (len(self.children) + 1)).pack(expand=True)
                        self.children.append(t)
                    log("child opened %d" % int(parts[1]))
            elif cmd == "busy":
                ms = int(parts[1])
                log("busy start %d" % ms)
                time.sleep(ms / 1000.0)
                log("busy end")
            elif cmd == "paint":
                for _ in range(int(parts[1])):
                    self.paint()
                    self.root.update_idletasks()
                log("painted %d" % int(parts[1]))
            elif cmd == "quit":
                log("quit")
                self.root.after(10, self.root.destroy)
                return
            else:
                log("unknown command %r" % line)
                return
        except Exception as ex:  # noqa: BLE001 — a bad command must not kill the guest
            log("command error %r: %s" % (line, ex))
            return
        log("ack %s" % line.strip())

    def menu_storm(self, n, delay):
        def step(i):
            if i >= n:
                log("menu storm done %d" % n)
                return
            if i % 2 == 0:
                self.post_menu()
            else:
                self.unpost_menu()
            self.root.after(delay, step, i + 1)
        step(0)

    def or_show(self, w, h):
        self.or_hide()
        t = tk.Toplevel(self.root)
        t.overrideredirect(True)
        x, y = self.root.winfo_rootx() + 30, self.root.winfo_rooty() + 30
        t.geometry("%dx%d+%d+%d" % (w, h, x, y))
        tk.Label(t, text="override-redirect, untyped", bg="#ffe080").pack(fill="both", expand=True)
        self.or_win = t
        log("or shown %dx%d" % (w, h))

    def or_hide(self):
        if self.or_win is not None:
            self.or_win.destroy()
            self.or_win = None
            log("or hidden")

    # --- control fifo -------------------------------------------------------
    def ctl_loop(self, path):
        # Reopen on EOF so a harness can open/write/close per command.
        while True:
            try:
                with open(path) as f:
                    for line in f:
                        self.root.after(0, self.handle, line)
            except FileNotFoundError:
                return
            time.sleep(0.02)

    def run(self):
        if self.args.ctl:
            if not os.path.exists(self.args.ctl):
                os.mkfifo(self.args.ctl)
            threading.Thread(target=self.ctl_loop, args=(self.args.ctl,), daemon=True).start()
        self.root.after(0, lambda: log("ready pid=%d display=%s" % (os.getpid(), os.environ.get("DISPLAY", ""))))
        self.root.mainloop()
        log("exit")


def main():
    global _logf
    ap = argparse.ArgumentParser()
    ap.add_argument("--ctl", help="control fifo path (created if missing)")
    ap.add_argument("--log", help="append GUEST: lines here (default $WASH_TESTGUEST_LOG)")
    ap.add_argument("--title", default="tkguest")
    ap.add_argument("--width", type=int, default=400)
    ap.add_argument("--height", type=int, default=300)
    args = ap.parse_args()
    path = args.log or os.environ.get("WASH_TESTGUEST_LOG")
    if path:
        _logf = open(path, "a", buffering=1)
    Guest(args).run()


if __name__ == "__main__":
    sys.exit(main())

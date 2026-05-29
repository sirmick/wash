/*
 * wash-vm initramfs init — minimal pivot to a squashfs-backed
 * overlay rootfs.
 *
 * The kernel hands control to PID 1 = this binary after running the
 * baked-in initramfs cpio. The job:
 *
 *   1. Mount the bare-minimum kernel filesystems (/proc, /sys, /dev)
 *      so subsequent mount calls work.
 *   2. Mount the squashfs at /dev/vda (TinyEMU's virtio-blk) read-only
 *      on /mnt/ro.
 *   3. Mount a tmpfs on /mnt/rw, partition into upperdir + workdir.
 *   4. Mount overlayfs combining /mnt/ro (lower) and /mnt/rw/up
 *      (upper) on /mnt/root.
 *   5. switch_root into /mnt/root and exec the real /sbin/init
 *      (busybox-init or whatever the buildroot rootfs ships).
 *
 * Static-linked against musl so it pulls no shared library, lives
 * in the initramfs cpio, weighs ~80-100 KiB. If you tinker with this
 * and the VM stalls at a blank kernel console, that's almost always
 * a typo here — re-read every mount() syscall arg.
 *
 * Anything that goes wrong: we write a banner to /dev/kmsg via
 * /proc/self/fd/N (no printf, no libc stdio init beyond what musl
 * does in _start) and hang. The kernel boot log will surface it.
 */

#include <sys/mount.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <linux/reboot.h>
#include <fcntl.h>
#include <unistd.h>
#include <string.h>
#include <errno.h>
#include <stdio.h>

/* glibc/musl don't expose pivot_root or switch_root via headers in a
   portable way; raw syscall is reliable. */
static int pivot_root(const char *new_root, const char *put_old) {
    return syscall(SYS_pivot_root, new_root, put_old);
}

/* keep an FD on /dev/kmsg open from the start so logging survives the
   pivot — re-opening /dev/kmsg AFTER pivot_root can fail if devtmpfs
   isn't reachable yet at the new path. */
static int kmsg_fd = -1;

static void say(const char *s) {
    if (kmsg_fd < 0) return;
    char line[256];
    int n = snprintf(line, sizeof(line), "<6>wash-init: %s\n", s);
    if (n > 0 && n < (int)sizeof(line)) write(kmsg_fd, line, n);
}

static void sayf(const char *fmt, int v) {
    if (kmsg_fd < 0) return;
    char line[256];
    int n = snprintf(line, sizeof(line), "<6>wash-init: ");
    int m = snprintf(line + n, sizeof(line) - n, fmt, v);
    if (m < 0) m = 0;
    line[n + m] = '\n';
    write(kmsg_fd, line, n + m + 1);
}

static void die(const char *s) {
    say("FATAL — hung. Reason:");
    say(s);
    sayf("errno=%d", errno);
    /* Spin so kernel doesn't panic with "init exited" and reboot. */
    while (1) pause();
}

#define M(src, tgt, fs, flags, data) \
    do { if (mount((src), (tgt), (fs), (flags), (data)) != 0) die("mount " tgt " " fs); } while (0)

int main(void) {
    /* Step 1: kernel filesystems. devtmpfs auto-mounts at /dev because
       CONFIG_DEVTMPFS_MOUNT=y, but only after the rootfs is up — at
       this point we're still on the initramfs ramfs, so we mount it
       ourselves so /dev/vda is visible. */
    mkdir("/proc", 0755);
    mkdir("/sys",  0755);
    mkdir("/dev",  0755);
    if (mount("none", "/dev",  "devtmpfs", 0, "mode=0755") != 0) {
        /* Can't log yet — kmsg_fd not open. Just hang. */
        while (1) pause();
    }
    /* Now we can log. */
    kmsg_fd = open("/dev/kmsg", O_WRONLY);
    say("starting");

    if (mount("none", "/proc", "proc",  0, NULL) != 0) die("mount /proc");
    if (mount("none", "/sys",  "sysfs", 0, NULL) != 0) die("mount /sys");

    say("kernel fs mounted");

    /* Step 2: squashfs lower layer. */
    mkdir("/mnt",      0755);
    mkdir("/mnt/ro",   0755);
    mkdir("/mnt/rw",   0755);
    mkdir("/mnt/root", 0755);
    M("/dev/vda", "/mnt/ro", "squashfs", MS_RDONLY, NULL);

    say("squashfs mounted at /mnt/ro");

    /* Step 3: tmpfs upper. 16 MiB is plenty for syslog writes,
       runtime sockets, /var/run, /tmp — even with the VM running
       for an hour, /var/log/messages is < 100 KiB and /tmp/wash-*
       sockets are inodes only. Bounded so a runaway log can't OOM
       the VM (which only has 64 MiB total). */
    M("none", "/mnt/rw", "tmpfs", 0, "size=16m,mode=0755");
    mkdir("/mnt/rw/up", 0755);
    mkdir("/mnt/rw/wk", 0755);

    say("tmpfs upper mounted at /mnt/rw");

    /* Step 4: overlayfs. */
    M("overlay", "/mnt/root", "overlay", 0,
      "lowerdir=/mnt/ro,upperdir=/mnt/rw/up,workdir=/mnt/rw/wk");

    say("overlay mounted at /mnt/root");

    /* Step 5: switch_root. The trick — we have to MOVE the /dev,
       /proc, /sys mounts under the new root so /sbin/init inherits
       them, then pivot_root, then unmount the old root. switch_root
       (the busybox util) bundles all this; we do it inline. */

    /* Move kernel fs under the new root so they survive the pivot. */
    mkdir("/mnt/root/dev", 0755);
    mkdir("/mnt/root/proc", 0755);
    mkdir("/mnt/root/sys", 0755);
    if (mount("/dev",  "/mnt/root/dev",  NULL, MS_MOVE, NULL) < 0) die("move /dev");
    if (mount("/proc", "/mnt/root/proc", NULL, MS_MOVE, NULL) < 0) die("move /proc");
    if (mount("/sys",  "/mnt/root/sys",  NULL, MS_MOVE, NULL) < 0) die("move /sys");

    say("switch_root via mount --move");

    /* Can't pivot_root away from rootfs (initramfs ramfs); the kernel
       returns EINVAL. The portable trick (what busybox switch_root and
       systemd's initrd-switch-root use) is mount --move <newroot> /
       then chroot. The kernel allows this for the initramfs case. */
    if (chdir("/mnt/root") < 0) die("chdir /mnt/root");
    if (mount(".", "/", NULL, MS_MOVE, NULL) < 0) die("mount --move . /");
    say("mount --move ok");
    if (chroot(".") < 0) die("chroot");
    say("chroot ok");
    if (chdir("/") < 0) die("chdir /");

    /* Re-open kmsg in the new root namespace — the inherited FD points
       at devtmpfs which followed the move, but better safe. */
    close(kmsg_fd);
    kmsg_fd = open("/dev/kmsg", O_WRONLY);
    say("execing /sbin/init");
    if (access("/sbin/init", X_OK) != 0) die("/sbin/init not executable");
    say("/sbin/init access ok");

    /* Off we go — buildroot's busybox-init takes it from here. */
    char *const argv[] = { "/sbin/init", NULL };
    char *const envp[] = { "HOME=/", "TERM=linux", "PATH=/sbin:/bin:/usr/sbin:/usr/bin", NULL };
    execve("/sbin/init", argv, envp);

    die("execve /sbin/init failed");
    return 1;
}

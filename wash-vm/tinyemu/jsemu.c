/*
 * JS emulator main
 * 
 * Copyright (c) 2016-2017 Fabrice Bellard
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in
 * all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL
 * THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
 * THE SOFTWARE.
 */
#include <stdlib.h>
#include <stdio.h>
#include <stdarg.h>
#include <string.h>
#include <inttypes.h>
#include <assert.h>
#include <fcntl.h>
#include <errno.h>
#include <unistd.h>
#include <time.h>
#include <emscripten.h>

#include "cutils.h"
#include "iomem.h"
#include "virtio.h"
#include "machine.h"
#include "list.h"
#include "fbuf.h"

void virt_machine_run(void *opaque);

/* provided in lib.js */
extern void console_write(void *opaque, const uint8_t *buf, int len);
extern void console_get_size(int *pw, int *ph);
/* wash patch: separate sink for the virtio-console device. JS-side
   dispatches to window.washVirtioConsole.onOutput so wash router
   frames stay off the HTIF/term channel. */
extern void virtio_console_out_write(void *opaque, const uint8_t *buf, int len);
extern void fb_refresh(void *opaque, void *data,
                       int x, int y, int w, int h, int stride);
extern void net_recv_packet(EthernetDevice *bs,
                            const uint8_t *buf, int len);

static uint8_t console_fifo[1024];
static int console_fifo_windex;
static int console_fifo_rindex;
static int console_fifo_count;
static BOOL console_resize_pending;

static int global_width;
static int global_height;
static VirtMachine *global_vm;
static BOOL global_carrier_state;

static int console_read(void *opaque, uint8_t *buf, int len)
{
    int out_len, l;
    len = min_int(len, console_fifo_count);
    console_fifo_count -= len;
    out_len = 0;
    while (len != 0) {
        l = min_int(len, sizeof(console_fifo) - console_fifo_rindex);
        memcpy(buf + out_len, console_fifo + console_fifo_rindex, l);
        len -= l;
        out_len += l;
        console_fifo_rindex += l;
        if (console_fifo_rindex == sizeof(console_fifo))
            console_fifo_rindex = 0;
    }
    return out_len;
}

/* called from JS */
void console_queue_char(int c)
{
    if (console_fifo_count < sizeof(console_fifo)) {
        console_fifo[console_fifo_windex] = c;
        if (++console_fifo_windex == sizeof(console_fifo))
            console_fifo_windex = 0;
        console_fifo_count++;
    }
}

/* called from JS */
void display_key_event(int is_down, int key_code)
{
    if (global_vm) {
        vm_send_key_event(global_vm, is_down, key_code);
    }
}

/* called from JS */
static int mouse_last_x, mouse_last_y, mouse_last_buttons;

void display_mouse_event(int dx, int dy, int buttons)
{
    if (global_vm) {
        if (vm_mouse_is_absolute(global_vm) || 1) {
            dx = min_int(dx, global_width - 1);
            dy = min_int(dy, global_height - 1);
            dx = (dx * VIRTIO_INPUT_ABS_SCALE) / global_width;
            dy = (dy * VIRTIO_INPUT_ABS_SCALE) / global_height;
        } else {
            /* relative mouse is not supported */
            dx = 0;
            dy = 0;
        }
        mouse_last_x = dx;
        mouse_last_y = dy;
        mouse_last_buttons = buttons;
        vm_send_mouse_event(global_vm, dx, dy, 0, buttons);
    }
}

/* called from JS */
void display_wheel_event(int dz)
{
    if (global_vm) {
        vm_send_mouse_event(global_vm, mouse_last_x, mouse_last_y, dz,
                            mouse_last_buttons);
    }
}

/* called from JS */
void net_write_packet(const uint8_t *buf, int buf_len)
{
    EthernetDevice *net = global_vm->net;
    if (net) {
        net->device_write_packet(net, buf, buf_len);
    }
}

/* called from JS */
void net_set_carrier(BOOL carrier_state)
{
    EthernetDevice *net;
    global_carrier_state = carrier_state;
    if (global_vm && global_vm->net) {
        net = global_vm->net;
        net->device_set_carrier(net, carrier_state);
    }
}

static void fb_refresh1(FBDevice *fb_dev, void *opaque,
                        int x, int y, int w, int h)
{
    int stride = fb_dev->stride;
    fb_refresh(opaque, fb_dev->fb_data + y * stride + x * 4, x, y, w, h,
               stride);
}

/* wash debug: ws/cli-triggered CPU+machine dump. Forward-declare here
   so we don't have to pull machine internals into jsemu.h. The browser
   side calls Module._wash_dump_global() in response to a `dump` ctl
   frame; stderr capture forwards the output as [tinyemu.stderr]. */
extern void wash_machine_dump_status(VirtMachine *m);
extern void wash_machine_dump_mem(VirtMachine *m, uint64_t paddr, uint32_t len);
void wash_dump_global(void)
{
    if (global_vm) wash_machine_dump_status(global_vm);
}
/* wash debug: physical-memory hex dump, callable from the FE on a
   `mem <addr> <len>` ctl request. Browser splits the u64 addr into a
   pair of u32s (Emscripten i64 marshalling is fragile across versions
   without -sEXPORT_ALL or BigInt builds, and a u32 high-half is plenty
   for our 256MB ram window). */
void wash_dump_mem_global(uint32_t paddr_hi, uint32_t paddr_lo, uint32_t len)
{
    if (!global_vm) return;
    uint64_t paddr = ((uint64_t)paddr_hi << 32) | paddr_lo;
    wash_machine_dump_mem(global_vm, paddr, len);
}

static CharacterDevice *console_init(void)
{
    CharacterDevice *dev;
    console_resize_pending = TRUE;
    dev = mallocz(sizeof(*dev));
    dev->write_data = console_write;
    dev->read_data = console_read;
    return dev;
}

/* ─────────────── wash patch: multi-channel virtio-console plumbing ──────────
 *
 * Each wash channel (data, log, future ctl etc.) gets its own
 * CharacterDevice + input fifo + VIRTIODevice. riscv_machine_init
 * instantiates one virtio-console MMIO device per channel; the run
 * loop below drains each fifo into the corresponding virtqueue.
 *
 *   FE → C  : window.washConsoles[ch].input(byte) calls
 *             _virtio_console_in_queue_char(ch, byte) which buffers
 *             in wash_vc_in_fifo[ch]; run loop drains.
 *
 *   C → FE  : virtio_console_write_data invokes the channel's
 *             CharacterDevice write_data, which goes to
 *             virtio_console_out_write(ch, …) (lib.js) — JS dispatches
 *             to window.washConsoles[ch].onOutput. */

#define WASH_VC_MAX  4
#define WASH_VC_FIFO 8192

static uint8_t wash_vc_in_fifo[WASH_VC_MAX][WASH_VC_FIFO];
static int wash_vc_in_windex[WASH_VC_MAX];
static int wash_vc_in_rindex[WASH_VC_MAX];
static int wash_vc_in_count[WASH_VC_MAX];

/* Per-channel device handles. CharacterDevice's opaque field stores
   the channel index so write_data/read_data know who they're for. */
static CharacterDevice *wash_vc_chardev[WASH_VC_MAX];
static VIRTIODevice    *wash_vc_vdev[WASH_VC_MAX];
static int              wash_vc_count;

void wash_virtio_console_set(int ch, VIRTIODevice *dev)
{
    if (ch >= 0 && ch < WASH_VC_MAX) wash_vc_vdev[ch] = dev;
}

static int wash_vc_in_read(void *opaque, uint8_t *buf, int len)
{
    int ch = (int)(intptr_t)opaque;
    int out_len, l;
    if (ch < 0 || ch >= WASH_VC_MAX) return 0;
    len = min_int(len, wash_vc_in_count[ch]);
    wash_vc_in_count[ch] -= len;
    out_len = 0;
    while (len != 0) {
        l = min_int(len, WASH_VC_FIFO - wash_vc_in_rindex[ch]);
        memcpy(buf + out_len, wash_vc_in_fifo[ch] + wash_vc_in_rindex[ch], l);
        len -= l;
        out_len += l;
        wash_vc_in_rindex[ch] += l;
        if (wash_vc_in_rindex[ch] == WASH_VC_FIFO) wash_vc_in_rindex[ch] = 0;
    }
    return out_len;
}

/* called from JS via cwrap: queue one byte for channel `ch`. */
void virtio_console_in_queue_char(int ch, int c)
{
    if (ch < 0 || ch >= WASH_VC_MAX) return;
    if (wash_vc_in_count[ch] < WASH_VC_FIFO) {
        wash_vc_in_fifo[ch][wash_vc_in_windex[ch]] = c;
        if (++wash_vc_in_windex[ch] == WASH_VC_FIFO) wash_vc_in_windex[ch] = 0;
        wash_vc_in_count[ch]++;
    }
}

/* Allocate a CharacterDevice for one wash virtio-console channel.
   Called from jsemu.c init; returned ptr is given to riscv_machine
   as the cs= argument to virtio_console_init. */
static CharacterDevice *virtio_console_dev_init(int ch)
{
    CharacterDevice *dev = mallocz(sizeof(*dev));
    /* opaque carries the channel index for both halves of the I/O. */
    dev->opaque     = (void *)(intptr_t)ch;
    dev->write_data = virtio_console_out_write;
    dev->read_data  = wash_vc_in_read;
    wash_vc_chardev[ch] = dev;
    return dev;
}

/* ─────────────────────── wash MULTIPORT virtio-console ───────────────────
   One MMIO device, N raw-chardev ports → /dev/vport0p0..vport0p{N-1}.
   Replaces the per-channel HVC pile for wash data / log / diag. FE side
   uses window.washVports[port].onOutput / .input — same shape as the
   existing washConsoles but routes through virtio_console_mp_*. */
#define WASH_MP_PORTS 3
#define WASH_MP_FIFO  8192
static uint8_t wash_mp_in_fifo[WASH_MP_PORTS][WASH_MP_FIFO];
static int wash_mp_in_windex[WASH_MP_PORTS];
static int wash_mp_in_rindex[WASH_MP_PORTS];
static int wash_mp_in_count[WASH_MP_PORTS];
static VIRTIODevice *wash_mp_vdev;

extern void virtio_vport_out_write(void *opaque, const uint8_t *buf, int len);

static int wash_mp_in_read(void *opaque, uint8_t *buf, int len)
{
    int port = (int)(intptr_t)opaque;
    int out_len, l;
    if (port < 0 || port >= WASH_MP_PORTS) return 0;
    len = min_int(len, wash_mp_in_count[port]);
    wash_mp_in_count[port] -= len;
    out_len = 0;
    while (len != 0) {
        l = min_int(len, WASH_MP_FIFO - wash_mp_in_rindex[port]);
        memcpy(buf + out_len, wash_mp_in_fifo[port] + wash_mp_in_rindex[port], l);
        len -= l;
        out_len += l;
        wash_mp_in_rindex[port] += l;
        if (wash_mp_in_rindex[port] == WASH_MP_FIFO) wash_mp_in_rindex[port] = 0;
    }
    return out_len;
}

/* Called from JS to inject a byte into one of the multiport device's
   per-port RX FIFOs. */
void virtio_vport_in_queue_char(int port, int c)
{
    if (port < 0 || port >= WASH_MP_PORTS) return;
    if (wash_mp_in_count[port] < WASH_MP_FIFO) {
        wash_mp_in_fifo[port][wash_mp_in_windex[port]] = c;
        if (++wash_mp_in_windex[port] == WASH_MP_FIFO) wash_mp_in_windex[port] = 0;
        wash_mp_in_count[port]++;
    }
}

static CharacterDevice *virtio_vport_dev_init(int port)
{
    CharacterDevice *dev = mallocz(sizeof(*dev));
    dev->opaque     = (void *)(intptr_t)port;
    dev->write_data = virtio_vport_out_write;
    dev->read_data  = wash_mp_in_read;
    return dev;
}

void wash_virtio_vport_set(VIRTIODevice *dev)
{
    wash_mp_vdev = dev;
}

typedef struct {
    VirtMachineParams *p;
    int ram_size;
    char *cmdline;
    BOOL has_network;
    char *pwd;
} VMStartState;

static void init_vm(void *arg);
static void init_vm_fs(void *arg);
static void init_vm_drive(void *arg);

void vm_start(const char *url, int ram_size, const char *cmdline,
              const char *pwd, int width, int height, BOOL has_network)
{
    VMStartState *s;

    s = mallocz(sizeof(*s));
    s->ram_size = ram_size;
    s->cmdline = strdup(cmdline);
    if (pwd)
        s->pwd = strdup(pwd);
    global_width = width;
    global_height = height;
    s->has_network = has_network;
    s->p = mallocz(sizeof(VirtMachineParams));
    virt_machine_set_defaults(s->p);
    virt_machine_load_config_file(s->p, url, init_vm_fs, s);
}

static void init_vm_fs(void *arg)
{
    VMStartState *s = arg;
    VirtMachineParams *p = s->p;

    if (p->fs_count > 0) {
        assert(p->fs_count == 1);
        p->tab_fs[0].fs_dev = fs_net_init(p->tab_fs[0].filename,
                                          init_vm_drive, s);
        if (s->pwd) {
            fs_net_set_pwd(p->tab_fs[0].fs_dev, s->pwd);
        }
    } else {
        init_vm_drive(s);
    }
}

static void init_vm_drive(void *arg)
{
    VMStartState *s = arg;
    VirtMachineParams *p = s->p;

    if (p->drive_count > 0) {
        assert(p->drive_count == 1);
        p->tab_drive[0].block_dev =
            block_device_init_http(p->tab_drive[0].filename,
                                   131072,
                                   init_vm, s);
    } else {
        init_vm(s);
    }
}

static void init_vm(void *arg)
{
    VMStartState *s = arg;
    VirtMachine *m;
    VirtMachineParams *p = s->p;
    int i;
    
    p->rtc_real_time = TRUE;
    p->ram_size = s->ram_size << 20;
    if (s->cmdline && s->cmdline[0] != '\0') {
        vm_add_cmdline(s->p, s->cmdline);
    }

    if (global_width > 0 && global_height > 0) {
        /* enable graphic output if needed */
        if (!p->display_device)
            p->display_device = strdup("simplefb");
        p->width = global_width;
        p->height = global_height;
    } else {
        p->console = console_init();
        /* wash: N independent CharacterDevices, one per wash virtio
           channel. riscv_machine_init instantiates a virtio-console
           MMIO device per non-null entry; Linux probes each as the
           next hvcN. Channels:
             ch 0 → hvc1 (legacy / future getty for login)
             ch 1 → hvc2 (wash router data plane, wash protocol frames)
             ch 2 → hvc3 (wash supervisor log + wash-router stdout)
           Bytes on each channel route to the matching
           window.washConsoles[ch] handler in JS. */
        /* wash patch: four virtio-console devices.
             ch 0 → hvc1 login (getty)
             ch 1 → hvc2 wash router data
             ch 2 → hvc3 wash supervisor + router stdout
             ch 3 → hvc4 in-VM diagnostics (ps/uptime/dmesg)        */
        wash_vc_count = 4;
        for (int i = 0; i < wash_vc_count; i++) {
            p->virtio_console[i] = virtio_console_dev_init(i);
        }
        /* Multiport virtio-console for wash data + log + diag — raw
           chardev semantics, no HVC/tty layer involvement on the
           guest side. */
        p->vport_count = WASH_MP_PORTS;
        for (int i = 0; i < WASH_MP_PORTS; i++) {
            p->virtio_vport_ports[i] = virtio_vport_dev_init(i);
        }
    }
    
    if (p->eth_count > 0 && !s->has_network) {
        /* remove the interfaces */
        for(i = 0; i < p->eth_count; i++) {
            free(p->tab_eth[i].ifname);
            free(p->tab_eth[i].driver);
        }
        p->eth_count = 0;
    }

    if (p->eth_count > 0) {
        EthernetDevice *net;
        int i;
        assert(p->eth_count == 1);
        net = mallocz(sizeof(EthernetDevice));
        net->mac_addr[0] = 0x02;
        for(i = 1; i < 6; i++)
            net->mac_addr[i] = (int)(emscripten_random() * 256);
        net->write_packet = net_recv_packet;
        net->opaque = NULL;
        p->tab_eth[0].net = net;
    }

    m = virt_machine_init(p);
    global_vm = m;

    virt_machine_free_config(s->p);

    if (m->net) {
        m->net->device_set_carrier(m->net, global_carrier_state);
    }
    
    free(s->p);
    free(s->cmdline);
    if (s->pwd) {
        memset(s->pwd, 0, strlen(s->pwd));
        free(s->pwd);
    }
    free(s);
    
    emscripten_async_call(virt_machine_run, m, 0);
}

/* need to be long enough to hide the non zero delay of setTimeout(_, 0) */
#define MAX_EXEC_TOTAL_CYCLE 3000000
#define MAX_EXEC_CYCLE        200000

#define MAX_SLEEP_TIME 10 /* in ms */

extern void washtrace_heartbeat(void);
void virt_machine_run(void *opaque)
{
    VirtMachine *m = opaque;
    int delay, i;
    FBDevice *fb_dev;

    washtrace_heartbeat();
    
    if (m->console_dev && virtio_console_can_write_data(m->console_dev)) {
        uint8_t buf[128];
        int ret, len;
        len = virtio_console_get_write_len(m->console_dev);
        len = min_int(len, sizeof(buf));
        ret = m->console->read_data(m->console->opaque, buf, len);
        if (ret > 0)
            virtio_console_write_data(m->console_dev, buf, ret);
        if (console_resize_pending) {
            int w, h;
            console_get_size(&w, &h);
            virtio_console_resize_event(m->console_dev, w, h);
            console_resize_pending = FALSE;
        }
    }

    /* wash patch: drain each wash virtio-console channel's input
       fifo (bytes arriving from JS) into the matching VIRTIODevice's
       recv queue. Mirrors the legacy single-device loop above. */
    for (int wi = 0; wi < wash_vc_count; wi++) {
        if (!wash_vc_vdev[wi] || !wash_vc_chardev[wi]) continue;
        if (!virtio_console_can_write_data(wash_vc_vdev[wi])) continue;
        uint8_t wbuf[128];
        int wret, wlen;
        wlen = virtio_console_get_write_len(wash_vc_vdev[wi]);
        wlen = min_int(wlen, sizeof(wbuf));
        wret = wash_vc_chardev[wi]->read_data(wash_vc_chardev[wi]->opaque, wbuf, wlen);
        if (wret > 0)
            virtio_console_write_data(wash_vc_vdev[wi], wbuf, wret);
    }

    /* wash multiport: drain per-port input FIFOs into the multiport
       device's per-port RX queues, and pump any pending control
       messages. */
    if (wash_mp_vdev) {
        virtio_console_mp_poll(wash_mp_vdev);
        for (int p = 0; p < WASH_MP_PORTS; p++) {
            if (wash_mp_in_count[p] == 0) continue;
            uint8_t buf[128];
            int len = wash_mp_in_read((void *)(intptr_t)p, buf, sizeof(buf));
            if (len > 0)
                virtio_console_mp_write_data(wash_mp_vdev, p, buf, len);
        }
    }

    fb_dev = m->fb_dev;
    if (fb_dev) {
        /* refresh the display */
        fb_dev->refresh(fb_dev, fb_refresh1, NULL);
    }
    
    i = 0;
    for(;;) {
        /* wait for an event: the only asynchronous event is the RTC timer */
        delay = virt_machine_get_sleep_duration(m, MAX_SLEEP_TIME);
        if (delay != 0 || i >= MAX_EXEC_TOTAL_CYCLE / MAX_EXEC_CYCLE)
            break;
        virt_machine_interp(m, MAX_EXEC_CYCLE);
        i++;
    }
    
    if (delay == 0) {
        emscripten_async_call(virt_machine_run, m, 0);
    } else {
        emscripten_async_call(virt_machine_run, m, MAX_SLEEP_TIME);
    }
}


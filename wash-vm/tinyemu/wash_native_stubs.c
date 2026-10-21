/* wash-vm native-build stubs.
 *
 * In the WASM build, these helpers live in jsemu.c and route the
 * per-channel virtio-console plumbing into the JS host. The native
 * temu binary doesn't have a JS bridge, so we provide no-op stubs
 * for the bookkeeping calls; the actual byte-stream wiring for
 * native is done in temu.c via its own CharacterDevice setup.
 */
#ifndef __EMSCRIPTEN__
#include <stddef.h>
#include <stdint.h>
#include <inttypes.h>
#include "cutils.h"
#include "virtio.h"

void wash_virtio_console_set(int ch, VIRTIODevice *dev) {
    (void)ch; (void)dev;
}
void wash_virtio_vport_set(VIRTIODevice *dev) {
    (void)dev;
}

/* Fill buf from the host RNG for the device tree's rng-seed (the WASM
   build's version in js/lib.js uses crypto.getRandomValues). Returns 0
   if none is available; the caller then leaves the seed out. */
#include <stdio.h>
int wash_host_random(uint8_t *buf, int len) {
    FILE *f = fopen("/dev/urandom", "rb");
    int ok;
    if (!f) return 0;
    ok = fread(buf, 1, len, f) == (size_t)len;
    fclose(f);
    return ok;
}
#endif

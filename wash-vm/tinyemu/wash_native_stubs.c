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
#endif

# TinyEMU WASM build for the wash RISC-V demo.
#
# Sibling of firmware/, kernel/, rootfs/ — produces the artifacts the
# browser side loads (riscvemu64-wasm.{js,wasm}) from C source in
# wash-vm/tinyemu/ + the lib.js shim in wash-vm/tinyemu/js/.
#
# Build context: wash-vm/tinyemu/ (passed by the image/Makefile target).
# The Dockerfile expects to find Makefile.js, the .c/.h sources, and
# js/lib.js inside the context root.
#
# Output: /out/riscvemu64-wasm.js + /out/riscvemu64-wasm.wasm,
# extracted by the Makefile via `docker run … cp`.

FROM emscripten/emsdk:3.1.74

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
        make ca-certificates \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /src

# Copy the entire tinyemu tree. .dockerignore excludes prebuilt
# artifacts (*.o, *.d, js/*-wasm.*) so we always start from a clean
# slate — guarantees the build sees the current lib.js + .c state.
COPY . /src/

# Wipe any straggler intermediates that survived .dockerignore (belt
# and braces) so emcc rebuilds everything from source.
RUN find . -maxdepth 2 \( -name '*.o' -o -name '*.d' \
        -o -name 'riscvemu*-wasm.js' -o -name 'riscvemu*-wasm.wasm' \) \
    -delete

RUN emmake make -f Makefile.js js/riscvemu64-wasm.js \
 && ls -lh js/riscvemu64-wasm.js js/riscvemu64-wasm.wasm

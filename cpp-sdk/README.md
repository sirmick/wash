# cpp-sdk — the wash C++ client

The reusable C++ side of the wash wire protocol, the counterpart to Go's
`internal/sdk`. A non-Go app (the wash-display compositor, an X/Wayland
bridge, anything native) links this to speak wash: handshake, framed
channels, cross-app `app_msg`, and the `--wash-manifest` probe.

Header-only except for one translation unit:

| File | What |
|---|---|
| `wash/wire.hpp` | Frame codec — `[flags:8][channel:24][len:32][payload]` (docs/WIRE.md §2). Dependency-free (POSIX + stdlib). |
| `wash/wire_conn.hpp` / `.cpp` | `WireConn`: threaded reader, handshake, `create_window`, cross-app `app_msg`, channel open/close. Needs `nlohmann/json`. |
| `wash/probe.hpp` | `write_probe(manifest, bundles)` — framed `--wash-manifest` output (header line + raw bundle bytes, no base64). Mirror of Go's `wire.WriteProbe`. |
| `third_party/nlohmann/json.hpp` | Vendored JSON (header-only). |

## Using it

Add to a CMake target:

```cmake
set(CPP_SDK ${CMAKE_CURRENT_SOURCE_DIR}/../cpp-sdk)
target_sources(my-app PRIVATE ${CPP_SDK}/wash/wire_conn.cpp)
target_include_directories(my-app PRIVATE ${CPP_SDK} ${CPP_SDK}/third_party)
```

Then `#include <wash/wire_conn.hpp>` / `#include <wash/probe.hpp>`.

Consumers: `wash-display/` (the compositor) and `examples/cpp-about/` (the
minimal template). See `examples/cpp-about/` for the smallest possible
app — handshake, receive an `app_msg`, send one back.

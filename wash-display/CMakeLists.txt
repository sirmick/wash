# wash-display compositor build (optional, native).
#
# This is gated behind WASH_DISPLAY=1 and is NEVER part of the
# CGO_ENABLED=0 Go core build (docs/DISPLAY.md §8). It links wlroots +
# codecs — native libs the Go side must never pull in.
#
# Build standalone:
#   cmake -S wash-display -B wash-display/build && cmake --build wash-display/build
#
# The wire client (main.cpp, wire.hpp, json.hpp) builds with NO external
# deps. The back-half (encoders/, encode.cpp, wsframe.hpp) needs libwebp.
# The compositor front-half (compositor.cpp, capture.cpp, ...) needs
# wlroots-0.17 + wayland-server + xkbcommon + pixman-1.

cmake_minimum_required(VERSION 3.16)
project(wash_display LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_RUNTIME_OUTPUT_DIRECTORY ${CMAKE_BINARY_DIR})

find_package(PkgConfig REQUIRED)

# --- back-half: image codec for the WS "video" channel --------------
# WebP rides the raw channel in-band (docs/DISPLAY.md §5); VP9/WebRTC
# (libdatachannel, vendored in tmp/mac-phoenix/subprojects) lands later.
pkg_check_modules(WEBP REQUIRED libwebp)

add_executable(wash-display
    src/main.cpp
    src/encoders/webp_encoder.cpp   # COPIED from mac-phoenix
    src/encode.cpp                  # SurfaceEncoder: capture buf -> framed msg
)
target_include_directories(wash-display PRIVATE
    src
    third_party               # nlohmann/json.hpp (header-only, vendored)
    ${WEBP_INCLUDE_DIRS}
)
target_link_libraries(wash-display PRIVATE ${WEBP_LIBRARIES})
target_link_directories(wash-display PRIVATE ${WEBP_LIBRARY_DIRS})

# --- front-half: wlroots compositor (commit 7+) ---------------------
# System wlroots on this target is 0.17.x (NOT the 0.18 the docs draft
# mentioned); 0.17 ships every XCB dep wlr_xwayland needs. Activated
# when compositor.cpp lands.
# pkg_check_modules(WLROOTS REQUIRED wlroots-0.17)
# pkg_check_modules(WAYLAND REQUIRED wayland-server)
# pkg_check_modules(XKB REQUIRED xkbcommon)
# pkg_check_modules(PIXMAN REQUIRED pixman-1)
# target_sources(wash-display PRIVATE
#     src/compositor.cpp   # wlroots: surfaces, xdg-shell, wlr_xwayland XWM
#     src/capture.cpp      # per-surface damaged buffers (pooled)
#     src/transport.cpp    # video channel write (libdatachannel/WS, later)
#     src/input.cpp        # wl_seat virtual pointer/keyboard from app_msg
#     src/clipboard.cpp    # wl_data_device + X11 selections <-> wash clipboard.*
# )
# target_include_directories(wash-display PRIVATE
#     ${WLROOTS_INCLUDE_DIRS} ${WAYLAND_INCLUDE_DIRS}
#     ${XKB_INCLUDE_DIRS} ${PIXMAN_INCLUDE_DIRS})
# target_link_libraries(wash-display PRIVATE
#     ${WLROOTS_LIBRARIES} ${WAYLAND_LIBRARIES}
#     ${XKB_LIBRARIES} ${PIXMAN_LIBRARIES})
# target_compile_definitions(wash-display PRIVATE WLR_USE_UNSTABLE)

# --- manifest sanity: `wash-display --wash-manifest` must stay valid JSON
# (CI greps this; see e2e/tests/display-cpp.spec.ts)

# wash-display — native X/Wayland app surfaces as wash windows.
#
# Two halves with very different dependencies:
#   - the wire client (src/main.cpp, wire.hpp, json.hpp): POSIX sockets
#     + stdlib only. Builds anywhere with a C++17 compiler. This is what
#     the default target below compiles and what the Makefile gates on
#     WASH_DISPLAY=1.
#   - the compositor (wlroots) + encode (libdatachannel/codecs): added
#     as optional sources guarded by find_package/pkg-config once those
#     libs are present (see the commented block). Not required to build
#     the wire client.
#
# Kept a separate CMake project (not the Go build) because it links
# native libs the pure-Go, CGO_ENABLED=0 wash core must never pull in.
cmake_minimum_required(VERSION 3.16)
project(wash_display CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
add_compile_options(-Wall -Wextra -O2)

add_executable(wash-display src/main.cpp)

# --- COMPOSITOR + ENCODE (optional; needs wlroots + libdatachannel) ---
# Uncomment on a box with the dev libs installed. These translation
# units fill the COMPOSITOR SEAM in main.cpp.
#
# find_package(PkgConfig REQUIRED)
# pkg_check_modules(WLROOTS REQUIRED wlroots-0.18)
# pkg_check_modules(WAYLAND REQUIRED wayland-server)
# pkg_check_modules(XKB REQUIRED xkbcommon)
# pkg_check_modules(DATACHANNEL REQUIRED libdatachannel)
# target_sources(wash-display PRIVATE
#   src/compositor.cpp   # wlroots: surfaces, xdg-shell, wlr_xwayland (XWM)
#   src/capture.cpp      # per-surface damaged-buffer capture
#   src/encode.cpp       # H264/VP9/PNG/WebP — copied from mac-phoenix
#   src/transport.cpp    # libdatachannel RTP + WS — copied from mac-phoenix
#   src/input.cpp        # app_msg input → wl_seat virtual pointer/keyboard
#   src/clipboard.cpp)   # wl_data_device + X11 selections <-> clipboard.*
# target_include_directories(wash-display PRIVATE
#   ${WLROOTS_INCLUDE_DIRS} ${WAYLAND_INCLUDE_DIRS} ${DATACHANNEL_INCLUDE_DIRS})
# target_link_libraries(wash-display PRIVATE
#   ${WLROOTS_LIBRARIES} ${WAYLAND_LIBRARIES} ${XKB_LIBRARIES} ${DATACHANNEL_LIBRARIES})

install(TARGETS wash-display RUNTIME DESTINATION bin)

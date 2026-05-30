# wash-display compositor build (optional, native).
#
# Gated behind WASH_DISPLAY=1 and NEVER part of the CGO_ENABLED=0 Go
# core build (docs/DISPLAY.md §8). Links wlroots + codecs — native libs
# the Go side must never pull in.
#
# Build standalone:
#   cmake -S wash-display -B wash-display/build && cmake --build wash-display/build
#
# Two halves, auto-detected:
#   - Always: the wire client (main.cpp + wire_conn.cpp) and the WebP
#     back-half (encoders/, encode.cpp) — needs only libwebp. This is
#     what CI builds; it proves the contract without a compositor.
#   - When wlroots-0.17 is present: compositor.cpp is added and
#     WASH_DISPLAY_COMPOSITOR is defined, so each real xdg toplevel
#     becomes a wash window. Absent wlroots, main.cpp falls back to the
#     fake "display_open" reference path.

cmake_minimum_required(VERSION 3.16)
project(wash_display LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_RUNTIME_OUTPUT_DIRECTORY ${CMAKE_BINARY_DIR})
add_compile_options(-Wall -Wextra -O2)

find_package(PkgConfig REQUIRED)

# --- back-half: image codec for the WS "video" channel --------------
# WebP rides the raw channel in-band (docs/DISPLAY.md §5); VP9/WebRTC
# (libdatachannel, vendored in tmp/mac-phoenix/subprojects) lands later.
pkg_check_modules(WEBP REQUIRED libwebp)

add_executable(wash-display
    src/main.cpp
    src/wire_conn.cpp               # threaded wire I/O (nlohmann/json)
    src/encoders/webp_encoder.cpp   # COPIED from mac-phoenix
    src/encode.cpp                  # SurfaceEncoder: capture buf -> framed msg
)
target_include_directories(wash-display PRIVATE
    src
    third_party                     # nlohmann/json.hpp (header-only, vendored)
    ${WEBP_INCLUDE_DIRS}
)
target_link_libraries(wash-display PRIVATE ${WEBP_LIBRARIES})
target_link_directories(wash-display PRIVATE ${WEBP_LIBRARY_DIRS})

# --- front-half: wlroots compositor (commit 7) ----------------------
# System wlroots is 0.17.x (NOT the 0.18 the docs draft mentioned); 0.17
# ships every XCB dep wlr_xwayland needs (commit 10). Auto-detected: if
# absent, the wire client + back-half still build. Distros disagree on
# the .pc name: Fedora/Arch ship versioned `wlroots-0.17`, Ubuntu 24.04
# ships the bare `wlroots` (= 0.17.1). Try the versioned name, fall back
# to the bare one.
pkg_check_modules(WLROOTS wlroots-0.17)
if(NOT WLROOTS_FOUND)
    pkg_check_modules(WLROOTS wlroots)
endif()
pkg_check_modules(WAYLAND wayland-server)
pkg_check_modules(XKB xkbcommon)
pkg_check_modules(PIXMAN pixman-1)

if(WLROOTS_FOUND AND WAYLAND_FOUND AND XKB_FOUND AND PIXMAN_FOUND)
    message(STATUS "wash-display: wlroots ${WLROOTS_VERSION} found — building compositor")

    # wlroots headers #include generated Wayland-protocol headers (e.g.
    # xdg-shell-protocol.h) that are NOT shipped — generate them with
    # wayland-scanner from the wayland-protocols XML into a build dir on
    # the include path. (Server-side headers: wlroots is a compositor.)
    find_program(WAYLAND_SCANNER wayland-scanner REQUIRED)
    pkg_get_variable(WP_PKGDATADIR wayland-protocols pkgdatadir)
    set(WP_PROTO_DIR ${CMAKE_BINARY_DIR}/wl-protocols)
    file(MAKE_DIRECTORY ${WP_PROTO_DIR})
    set(WP_GENERATED "")
    # name -> path-under-pkgdatadir
    foreach(proto
        "xdg-shell=stable/xdg-shell/xdg-shell.xml")
        string(REPLACE "=" ";" _p ${proto})
        list(GET _p 0 _name)
        list(GET _p 1 _rel)
        add_custom_command(
            OUTPUT ${WP_PROTO_DIR}/${_name}-protocol.h
            COMMAND ${WAYLAND_SCANNER} server-header
                    ${WP_PKGDATADIR}/${_rel} ${WP_PROTO_DIR}/${_name}-protocol.h
            DEPENDS ${WP_PKGDATADIR}/${_rel}
            VERBATIM)
        list(APPEND WP_GENERATED ${WP_PROTO_DIR}/${_name}-protocol.h)
    endforeach()
    add_custom_target(wl-protocols DEPENDS ${WP_GENERATED})
    add_dependencies(wash-display wl-protocols)

    target_sources(wash-display PRIVATE
        src/compositor.cpp          # wlroots: headless backend, xdg-shell
        # commit 8+: capture.cpp / transport.cpp / input.cpp / clipboard.cpp
    )
    target_compile_definitions(wash-display PRIVATE WASH_DISPLAY_COMPOSITOR WLR_USE_UNSTABLE)
    target_include_directories(wash-display PRIVATE
        ${WP_PROTO_DIR}
        ${WLROOTS_INCLUDE_DIRS} ${WAYLAND_INCLUDE_DIRS}
        ${XKB_INCLUDE_DIRS} ${PIXMAN_INCLUDE_DIRS})
    target_link_libraries(wash-display PRIVATE
        ${WLROOTS_LIBRARIES} ${WAYLAND_LIBRARIES}
        ${XKB_LIBRARIES} ${PIXMAN_LIBRARIES})
    target_link_directories(wash-display PRIVATE
        ${WLROOTS_LIBRARY_DIRS} ${WAYLAND_LIBRARY_DIRS}
        ${XKB_LIBRARY_DIRS} ${PIXMAN_LIBRARY_DIRS})
else()
    message(STATUS "wash-display: wlroots not found — wire client + WebP back-half only")
endif()

install(TARGETS wash-display RUNTIME DESTINATION bin)

# --- manifest sanity: `wash-display --wash-manifest` must stay valid JSON
# (CI greps this; see e2e/tests/display-cpp.spec.ts)

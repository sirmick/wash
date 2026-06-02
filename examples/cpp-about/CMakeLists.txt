# cpp-about — minimal native wash app on cpp-sdk.
cmake_minimum_required(VERSION 3.16)
project(cpp_about LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_RUNTIME_OUTPUT_DIRECTORY ${CMAKE_BINARY_DIR})
add_compile_options(-Wall -Wextra -O2)

# The reusable wash C++ client lives at the repo root.
set(CPP_SDK ${CMAKE_CURRENT_SOURCE_DIR}/../../cpp-sdk)

# Embed assets/index.js as raw bytes (the FE bundle) — the same base64-free
# pattern wash-display uses. `extern const` gives external linkage.
set(INDEX_JS "${CMAKE_CURRENT_SOURCE_DIR}/assets/index.js")
set(INDEX_GEN "${CMAKE_CURRENT_BINARY_DIR}/main_bundle.cpp")
file(READ "${INDEX_JS}" IDX_HEX HEX)
string(LENGTH "${IDX_HEX}" IDX_HEXLEN)
math(EXPR IDX_LEN "${IDX_HEXLEN} / 2")
string(REGEX REPLACE "(..)" "0x\\1," IDX_BYTES "${IDX_HEX}")
file(WRITE "${INDEX_GEN}"
    "extern const unsigned char wash_index_js[] = { ${IDX_BYTES} };\n"
    "extern const unsigned int wash_index_js_len = ${IDX_LEN};\n")

add_executable(cpp-about
    main.cpp
    ${CPP_SDK}/wash/wire_conn.cpp   # the wash wire client (threaded I/O)
    ${INDEX_GEN}                    # embedded FE bundle (raw bytes)
)
target_include_directories(cpp-about PRIVATE
    ${CPP_SDK}                      # <wash/wire_conn.hpp>, <wash/probe.hpp>
    ${CPP_SDK}/third_party          # nlohmann/json.hpp
)

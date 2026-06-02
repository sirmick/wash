// wash framed-probe writer (C++ mirror of Go's wire.WriteProbe).
//
// `<binary> --wash-manifest` writes framed output to stdout: a single
// header JSON line (the manifest + a descriptor per raw bundle frame),
// then each bundle's raw bytes concatenated in order — no base64. The
// router (internal/wire ReadProbe) splits on the first newline, reads the
// header, then reads each declared byte run.
//
// Dependency: nlohmann/json (header-only, vendored under cpp-sdk).
#pragma once

#include <cstddef>
#include <cstdio>
#include <string>
#include <vector>

#include <nlohmann/json.hpp>

namespace wash {

// Bundle is one raw blob shipped after the header (e.g. a settings panel
// bundle). A zero-length bundle is skipped entirely.
struct Bundle {
    std::string kind;          // "main" | "panel"
    const unsigned char* data; // raw bytes (not owned)
    std::size_t len;
};

// write_probe emits the framed --wash-manifest output to stdout: the
// header line then the raw bundle bytes. Mirrors Go's wire.WriteProbe.
inline void write_probe(const nlohmann::json& manifest,
                        const std::vector<Bundle>& bundles = {}) {
    nlohmann::json hdr;
    hdr["manifest"] = manifest;
    nlohmann::json frames = nlohmann::json::array();
    for (const auto& b : bundles) {
        if (b.len == 0) continue;
        frames.push_back({{"kind", b.kind}, {"len", b.len}});
    }
    if (!frames.empty()) hdr["bundles"] = frames;

    const std::string line = hdr.dump();
    std::fwrite(line.data(), 1, line.size(), stdout);
    std::fputc('\n', stdout);
    for (const auto& b : bundles) {
        if (b.len == 0) continue;
        std::fwrite(b.data, 1, b.len, stdout);
    }
}

} // namespace wash

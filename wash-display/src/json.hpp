// Deliberately tiny JSON helpers — enough for the handful of flat
// messages wash-display exchanges (identity / identity.ack /
// window.create / window.created / app_msg). NOT a general JSON
// library; if message shapes grow, vendor a real parser.
#pragma once

#include <cstdint>
#include <string>

namespace wash::json {

// esc escapes a string for embedding in a JSON double-quoted value.
inline std::string esc(const std::string& s) {
    std::string out;
    out.reserve(s.size() + 2);
    for (char c : s) {
        switch (c) {
            case '"': out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\n': out += "\\n"; break;
            case '\r': out += "\\r"; break;
            case '\t': out += "\\t"; break;
            default: out += c;
        }
    }
    return out;
}

// find_string extracts the value of "key":"..." within src (first
// match). Returns true + value on success. Naive: no nested-escape
// handling beyond \" — adequate for our control messages.
inline bool find_string(const std::string& src, const std::string& key, std::string& out) {
    std::string needle = "\"" + key + "\"";
    size_t k = src.find(needle);
    if (k == std::string::npos) return false;
    size_t c = src.find(':', k + needle.size());
    if (c == std::string::npos) return false;
    size_t q = src.find('"', c + 1);
    if (q == std::string::npos) return false;
    out.clear();
    for (size_t i = q + 1; i < src.size(); ++i) {
        if (src[i] == '\\' && i + 1 < src.size()) { out += src[i + 1]; ++i; continue; }
        if (src[i] == '"') return true;
        out += src[i];
    }
    return false;
}

// find_uint extracts "key":<number> (integer). Returns true on success.
inline bool find_uint(const std::string& src, const std::string& key, uint64_t& out) {
    std::string needle = "\"" + key + "\"";
    size_t k = src.find(needle);
    if (k == std::string::npos) return false;
    size_t c = src.find(':', k + needle.size());
    if (c == std::string::npos) return false;
    size_t i = c + 1;
    while (i < src.size() && (src[i] == ' ' || src[i] == '\t')) ++i;
    if (i >= src.size() || src[i] < '0' || src[i] > '9') return false;
    uint64_t v = 0;
    for (; i < src.size() && src[i] >= '0' && src[i] <= '9'; ++i) v = v * 10 + (src[i] - '0');
    out = v;
    return true;
}

// data_object returns the substring of the "data":{...} value (the
// nested app_msg payload), or empty if absent. Brace-balanced.
inline std::string data_object(const std::string& src) {
    size_t k = src.find("\"data\"");
    if (k == std::string::npos) return "";
    size_t b = src.find('{', k);
    if (b == std::string::npos) return "";
    int depth = 0;
    for (size_t i = b; i < src.size(); ++i) {
        if (src[i] == '{') ++depth;
        else if (src[i] == '}') {
            if (--depth == 0) return src.substr(b, i - b + 1);
        }
    }
    return "";
}

} // namespace wash::json

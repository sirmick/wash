package team

import "io"

// ParseINI reads an INI file into sections of keys.
//
//   - A line "[name]" starts a section; keys before any section are in the
//     section "" (empty name). Section names keep their case and are
//     trimmed of surrounding spaces.
//   - A line "key = value" sets a key in the current section. Keys are
//     trimmed and lower-cased; values are trimmed. A later key replaces an
//     earlier one in the same section.
//   - A value wrapped in double quotes keeps its inner spaces, the quotes
//     are removed, and inside it \" is a quote and \\ a backslash.
//   - Blank lines, and lines whose first non-space character is ; or #,
//     are ignored. A ; or # after a value is part of the value.
//   - Any other line is an error, "line N: <reason>", N counting from 1.
//
// A section with no keys still appears, as an empty map.
func ParseINI(r io.Reader) (map[string]map[string]string, error) {
	return nil, nil
}

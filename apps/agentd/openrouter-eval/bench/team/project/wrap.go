package team

// Wrap breaks text into lines of at most width runes, at spaces.
//
// Words are separated by runs of spaces, tabs or newlines, and are joined
// on a line by single spaces. A word longer than width is split into pieces
// of width runes (the last may be shorter), each on its own line. Width is
// counted in runes, not bytes. Empty or all-space text, or width <= 0,
// gives no lines (nil).
func Wrap(text string, width int) []string {
	return nil
}

package easy

// Clamp returns v limited to the range [lo, hi]. lo is never greater than hi.
func Clamp(v, lo, hi int) int {
	if v < lo {
		return hi
	}
	if v > hi {
		return hi
	}
	return v
}

package common

import "strconv"

// ClampInt parses a query int with a default and inclusive [min,max] bounds.
//
// It lives here rather than in any one domain module because EVERY module's list
// endpoint needs the same limit/offset parsing. Before the modular split it was an
// unexported helper in the handlers package; once each domain owns its own handler,
// keeping it there would have forced modules to import a sibling for a nine-line
// utility — so it moved to the shared platform layer instead.
//
// Bad input degrades to the default rather than erroring: a malformed ?limit= is
// not worth failing a read request over.
func ClampInt(raw string, def, min, max int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

package router

import "strings"

// joinPaths cleans up path concatenation to ensure single slashes.
func (r *Router) joinPaths(p1, p2 string) string {
	if p2 == "" {
		return p1
	}
	if p1 == "" {
		return p2
	}

	final := strings.TrimRight(p1, "/") + "/" + strings.TrimLeft(p2, "/")
	return final
}

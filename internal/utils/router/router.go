package router

import (
	"net/http"
	"strings"
)

type Middleware func(next http.Handler) http.Handler

type Router struct {
	mux         *http.ServeMux
	middlewares []Middleware
	prefix      string
}

func NewRouter() *Router {
	return &Router{
		mux:         http.NewServeMux(),
		middlewares: []Middleware{},
		prefix:      "",
	}
}

// Use adds global middleware to the router.
func (r *Router) Use(mw ...Middleware) {
	r.middlewares = append(r.middlewares, mw...)
}

// Group creates a new sub-router with a specific prefix and optional group-level middleware.
// The fn closure allows you to define routes belonging to this group.
func (r *Router) Group(prefix string, fn func(*Router), middlewares ...Middleware) {
	// Create a new router instance that shares the original mux
	// but has its own prefix and middleware stack.
	newGroup := &Router{
		mux:         r.mux,
		middlewares: r.chain(middlewares...), // Inherit parent middleware + add new ones
		prefix:      r.joinPaths(r.prefix, prefix),
	}

	fn(newGroup)
}

func (r *Router) Middleware(middlewares ...Middleware) *Router {
	return &Router{
		mux:         r.mux,
		middlewares: r.chain(middlewares...), // Inherit parent middleware + add new ones
		prefix:      r.prefix,
	}
}

// chain returns this router's middleware stack followed by extra, always in
// freshly allocated storage.
//
// The allocation is the point: appending straight onto r.middlewares reuses that
// slice's spare capacity, so two sub-routers derived from the same parent would
// write their own middleware into the same array slot and silently inherit each
// other's. That is invisible until a second sub-router is derived from a parent
// that has spare capacity — exactly what happens once several domains mount
// their own groups off a shared router.
func (r *Router) chain(extra ...Middleware) []Middleware {
	chained := make([]Middleware, 0, len(r.middlewares)+len(extra))
	chained = append(chained, r.middlewares...)

	return append(chained, extra...)
}

func (r *Router) Handle(method, path string, handler http.HandlerFunc) {
	fullPath := r.joinPaths(r.prefix, path)

	// ensure path starts with /
	if !strings.HasPrefix(fullPath, "/") {
		fullPath = "/" + fullPath
	}

	// Since Go 1.22, we can specify methods in the pattern string (e.g., "GET /users")
	// If method is provided, prepend it.
	pattern := fullPath
	if method != "" {
		pattern = method + " " + fullPath
	}

	// Chain middleware: Last added runs first (wrapping the handler)
	// We iterate backwards so the first middleware added is the "outermost" layer.
	finalHandler := http.Handler(handler)
	for i := len(r.middlewares) - 1; i >= 0; i-- {
		finalHandler = r.middlewares[i](finalHandler)
	}

	r.mux.Handle(pattern, finalHandler)
}

func (r *Router) HandleFunc(path string, handler http.HandlerFunc) {
	method, pattern, found := strings.Cut(path, " ")
	if !found {
		// No method specified, treat entire path as pattern
		// 		since Cut returns pattern as empty if not found, we switch them here
		method = ""
		pattern = path
	}

	r.Handle(method, pattern, handler)
}

// Helper methods for common HTTP verbs
func (r *Router) GET(path string, handler http.HandlerFunc) {
	r.Handle("GET", path, handler)
}

func (r *Router) POST(path string, handler http.HandlerFunc) {
	r.Handle("POST", path, handler)
}

func (r *Router) PUT(path string, handler http.HandlerFunc) {
	r.Handle("PUT", path, handler)
}

func (r *Router) DELETE(path string, handler http.HandlerFunc) {
	r.Handle("DELETE", path, handler)
}

// ServeHTTP implements the http.Handler interface, allowing the Router to be passed to http.ListenAndServe.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

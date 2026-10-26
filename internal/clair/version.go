package clair

// Version is stamped at build time by the Makefile and the Dockerfile
// (-ldflags "-X github.com/erancihan/clair/internal/clair.Version=…"). "dev"
// means the binary was built without it, which is how a `go run` reports.
var Version = "dev"

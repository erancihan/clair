package web

import (
	"embed"
	"os"
)

//go:generate go tool github.com/a-h/templ/cmd/templ generate

var (
	//go:embed static
	Static embed.FS
)

/**
 * Returns the public folder path.
 * Path can be used to serve public files.
 * Path can be set from environment variable.
 */
func Public() string {
	// get public path from environment variable
	publicPath := os.Getenv("PUBLIC_PATH")
	if publicPath == "" {
		publicPath = "web/public"
	}

	return publicPath
}

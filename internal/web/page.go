package web

import "github.com/a-h/templ"

// Base renders body inside the site shell (head, header, footer).
//
// This is the only thing internal/web exports on behalf of the pages: page
// components live in internal/web/pages and are referenced from there directly.
// The package deliberately does NOT re-export them - a single file naming every
// page in the application is a file every domain has to edit, and it buys
// nothing over importing the pages package.
func Base(title string, body templ.Component) templ.Component {
	return base(title, body)
}

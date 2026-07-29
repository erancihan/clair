package web

import (
	"github.com/a-h/templ"

	"github.com/erancihan/clair/internal/web/components"
)

// PageUser is the signed-in visitor the shell renders a user menu for. It is an
// alias so callers assembling a Page never have to name internal/web/components.
type PageUser = components.SessionUser

// Page is the per-request state of the site shell that Base renders around a
// page body.
//
// Assemble it with the authentication layer's PageShell helper rather than by
// hand: PageShell is what mints and persists the CSRF token, and resolving the
// identity there is what keeps internal/web free of any dependency on
// internal/server/authentication - which is what lets that layer render the
// login page with no import cycle.
type Page struct {
	// Title is the document title.
	Title string
	// CSRFToken is published as <meta name="csrf-token"> so browser JS can read
	// it and echo it back in the X-CSRF-Token header on mutating requests. It
	// must be the token bound to the caller's session and already persisted to
	// the session cookie, or the CSRF middleware will reject what JS sends.
	CSRFToken string
	// User is the signed-in visitor, or nil for an anonymous one - in which case
	// the shell renders a sign-in link in place of the user menu.
	User *PageUser
}

// Base renders body inside the site shell (head, header, footer).
//
// This is the only thing internal/web exports on behalf of the pages: page
// components live in internal/web/pages and are referenced from there directly.
// The package deliberately does NOT re-export them - a single file naming every
// page in the application is a file every domain has to edit, and it buys
// nothing over importing the pages package.
func Base(page Page, body templ.Component) templ.Component {
	return base(page, body)
}

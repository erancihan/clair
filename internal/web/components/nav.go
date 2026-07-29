package components

// NavLink is one entry in the site's primary navigation.
type NavLink struct {
	// Href is the path the entry links to.
	Href string
	// Label is the text rendered for the entry.
	Label string
}

// NavLinks is the single source of truth for the primary navigation. The desktop
// and mobile renderers both walk this list, so putting a domain in the nav is one
// line here rather than a matching edit in every renderer — which is what made
// the nav a guaranteed conflict between domains adding a link at the same time.
//
// Keep the entries in the order they should appear.
var NavLinks = []NavLink{
	{Href: "/", Label: "Home"},
	{Href: "/games", Label: "Games"},
}

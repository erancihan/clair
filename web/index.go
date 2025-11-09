package web

import (
	"github.com/a-h/templ"

	pages "github.com/erancihan/clair/web/pages"
)

func Base(title string, body templ.Component) templ.Component {
	return base(title, body)
}

func Home() templ.Component {
	return pages.Home()
}

func Login() templ.Component {
	return pages.LoginPage()
}

func NotFound() templ.Component {
	return pages.NotFound()
}

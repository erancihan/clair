package games

import (
	"net/http"

	"github.com/a-h/templ"
	api_auth "github.com/erancihan/clair/internal/server/authentication"
	server_context "github.com/erancihan/clair/internal/server/context"
	"github.com/erancihan/clair/internal/utils/router"
	"github.com/erancihan/clair/internal/web"
	"github.com/erancihan/clair/internal/web/pages"
)

// Mount registers every route the games domain owns onto r.
//
// This is the convention every domain package follows:
//
//	func Mount(r *router.Router, ctx server_context.BackEndContext)
//
// server.Routes calls it once and never learns anything about what is inside.
// A domain owns its whole path space, its own middleware and its own handlers,
// so adding, renaming or removing a route is a change in this package alone -
// not an edit to the one function every other domain also has to edit.
//
// Two things to copy when writing the next one:
//
//   - Group the domain under its own prefix and register everything inside that
//     closure. r arrives already carrying the application-wide middleware, so
//     anything added here is additive.
//
//   - Mutating routes belong behind api_auth.CSRF(), mounted by the domain on
//     its own group - it is deliberately not mounted globally, because login and
//     registration precede the session a token is bound to. The pattern is:
//
//     r.Group("orders", func(orders *router.Router) {
//     orders.HandleFunc("POST /", createOrder(ctx))
//     }, api_auth.CSRF())
//
//     The token to send back is published by the shell as
//     <meta name="csrf-token">; see web.Page.
//
// The games POST routes below are pointedly NOT behind CSRF: they predate the
// token being reachable from the browser, and wrapping them now would break
// every client that has not been taught to send one. That is the chess domain's
// call to make, together with the front-end change that goes with it.
func Mount(r *router.Router, ctx server_context.BackEndContext) {
	r.Group("games", func(games *router.Router) {
		games.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			templ.Handler(web.Base(api_auth.PageShell(w, r, "Games"), pages.Games())).ServeHTTP(w, r)
		})

		games.Group("tic-tac-toe", func(route *router.Router) {
			route.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
				templ.Handler(web.Base(api_auth.PageShell(w, r, "Tic-Tac-Toe"), pages.TicTacToe())).ServeHTTP(w, r)
			})
			route.HandleFunc("POST /create", TicTacToe.CreateGame(ctx))
			route.HandleFunc("GET /stream", TicTacToe.StreamGame(ctx))
			route.HandleFunc("POST /move", TicTacToe.TakeAction(ctx))
		})

		games.Group("chess", func(route *router.Router) {
			route.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
				templ.Handler(web.Base(api_auth.PageShell(w, r, "Chess"), pages.Chess())).ServeHTTP(w, r)
			})
			route.HandleFunc("POST /create", Chess.CreateGame(ctx))
			route.HandleFunc("GET /stream", Chess.StreamGame(ctx))
			route.HandleFunc("POST /move", Chess.TakeAction(ctx))
		})
	})
}

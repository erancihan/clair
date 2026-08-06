package games

import (
	"context"
	"net/http"

	"github.com/a-h/templ"
	api_auth "github.com/erancihan/clair/internal/server/authentication"
	server_context "github.com/erancihan/clair/internal/server/context"
	"github.com/erancihan/clair/internal/utils/router"
	"github.com/erancihan/clair/internal/web"
	"github.com/erancihan/clair/internal/web/pages"
)

// mountChess registers the chess routes onto r, which is the games group.
//
// Chess owns more routes than the other games (a join flow, matchmaking, PGN
// export and an owner lookup), so its wiring lives here instead of inline in
// Mount. That keeps mount.go short enough to stay readable as the example other
// domains copy.
//
// The POST routes are deliberately NOT behind api_auth.CSRF(). A move is
// authorised by the seat token in the request body - a bearer credential a
// browser does not attach on its own - so a cross-site POST cannot make a move
// on anybody's behalf. Mounting CSRF here without teaching the front end to send
// the X-CSRF-Token header would break every client mid-game, and it would buy
// nothing while no route mutates state that is authorised by a cookie alone.
//
// Adopt it, in one change together with the front end, as soon as either of
// these lands:
//
//   - a route that forfeits or destroys state tied to an owner (resign, abandon
//     or delete a game) - forging one of those is real harm, not griefing;
//   - durable, user-visible game history (ratings, leaderboards).
//
// The token to send is published by the page shell as <meta name="csrf-token">.
func mountChess(r *router.Router, ctx server_context.BackEndContext) {
	// The janitor evicts finished and abandoned games from the in-memory store.
	// The seam hands a domain no lifecycle context, so this uses the background
	// context: the loop lives as long as the process, which is exactly as long as
	// the store it prunes. StartChessCleanup is idempotent.
	StartChessCleanup(context.Background())

	r.Group("chess", func(route *router.Router) {
		route.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			templ.Handler(web.Base(api_auth.PageShell(w, r, "Chess"), pages.Chess())).ServeHTTP(w, r)
		})
		route.HandleFunc("POST /create", Chess.CreateGame(ctx))
		route.HandleFunc("POST /join", Chess.JoinGame(ctx))
		route.HandleFunc("GET /open", Chess.OpenGames(ctx))
		route.HandleFunc("GET /mine", Chess.MyGames(ctx))
		route.HandleFunc("GET /stream", Chess.StreamGame(ctx))
		route.HandleFunc("POST /move", Chess.TakeAction(ctx))
		route.HandleFunc("GET /pgn", Chess.PGN(ctx))
	})
}

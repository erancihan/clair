package authentication

import (
	"net/http"

	server_context "github.com/erancihan/clair/internal/server/context"
)

func AuthLogout(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, _ := store.Get(r, SESSION_NAME)

		session.Options.MaxAge = -1
		session.Save(r, w)
	}
}

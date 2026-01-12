package authentication

import (
	"net/http"

	"github.com/erancihan/clair/internal/database/models"
	server_context "github.com/erancihan/clair/internal/server/context"
	"gorm.io/gorm"
)

// TODO:
// - what are the best practices for session management, expiration, and security?
// - enhance middleware to support roles/permissions

func AuthMiddleware(ctx server_context.BackEndContext) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			session, err := store.Get(r, SESSION_NAME)
			if err != nil {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			auth, ok := session.Values["authenticated"].(bool)
			if !ok || !auth {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// validate user exists
			userID, ok := session.Values["id"].(uint)
			if !ok {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			tx := ctx.DBConn.Session(&gorm.Session{Context: r.Context()})

			var user models.User
			result := tx.Limit(1).Where("id = ?", userID).Find(&user)
			if result.RowsAffected == 0 {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// proceed to next handler
			next.ServeHTTP(w, r)
		})
	}
}

package authentication

import (
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/erancihan/clair/internal/database/models"
	server_context "github.com/erancihan/clair/internal/server/context"
	"github.com/erancihan/clair/internal/web"
	"gorm.io/gorm"
)

func LoginPage(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		templ.Handler(web.Base("Clair", web.Login())).ServeHTTP(w, r)
	}
}

func AuthLogin(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// handle login form submission
		// get email and password from form
		email := strings.ToLower(r.FormValue("email"))
		password := r.FormValue("password")

		// validate email and password
		if email == "" || password == "" {
			http.Error(w, "Email and password are required", http.StatusBadRequest)
			return
		}

		// authenticate user

		var user models.User

		tx := ctx.DBConn.Session(&gorm.Session{Context: r.Context()})

		result := tx.Limit(1).Where("email = ?", email).Find(&user)
		if result.RowsAffected == 0 {
			http.Error(w, "Invalid email or password", http.StatusUnauthorized)
			return
		}

		// check password
		// TODO:

		// if fails, return error
		// TODO:

		// otherwise, redirect with session cookie
		http.SetCookie(w, &http.Cookie{
			Name:  "session_token",
			Value: "some_session_token", // TODO: generate real session token
		})
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	}
}

func AuthLogout(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// handle logout
	}
}

func AuthRegister(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// handle registration
	}
}

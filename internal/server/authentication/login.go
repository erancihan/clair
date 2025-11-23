package authentication

import (
	"encoding/json"
	"net/http"

	"github.com/a-h/templ"
	"github.com/erancihan/clair/internal/database/models"
	server_context "github.com/erancihan/clair/internal/server/context"
	"github.com/erancihan/clair/internal/web"
	"github.com/gorilla/sessions"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const SESSION_NAME = "session-name"

var store = sessions.NewCookieStore([]byte("super-secret-32-byte-key-auth-v1"))

func LoginPage(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		templ.Handler(web.Base("Clair", web.Login())).ServeHTTP(w, r)
	}
}

type LoginPayload struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func AuthLogin(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var creds LoginPayload
		if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
			http.Error(w, "Invalid request payload", http.StatusBadRequest)
			return
		}

		// lookup user in database

		tx := ctx.DBConn.Session(&gorm.Session{Context: r.Context()})

		var user models.User
		result := tx.Limit(1).Where("email = ?", creds.Email).Find(&user)
		if result.RowsAffected == 0 {
			http.Error(w, "Invalid email or password", http.StatusUnauthorized)
			return
		}

		// check password
		if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(creds.Password)); err != nil {
			http.Error(w, "Invalid email or password", http.StatusUnauthorized)
			return
		}

		// ---- create session ----
		session, _ := store.Get(r, SESSION_NAME)
		session.Values["authenticated"] = true
		session.Values["id"] = user.ID

		session.Options = &sessions.Options{
			Path:     "/",
			MaxAge:   3600,
			HttpOnly: true,
			Secure:   false,
			SameSite: http.SameSiteLaxMode,
		}

		session.Save(r, w)
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	}
}

func AuthLogout(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, _ := store.Get(r, SESSION_NAME)

		session.Options.MaxAge = -1
		session.Save(r, w)
	}
}

type RegisterPayload struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func AuthRegister(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// registered as POST only

		var creds RegisterPayload
		if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
			http.Error(w, "Invalid request payload", http.StatusBadRequest)
			return
		}

		hashedPass, err := bcrypt.GenerateFromPassword([]byte(creds.Password), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, "Server error", http.StatusInternalServerError)
			return
		}

		user := models.User{
			Username: creds.Username,
			Email:    creds.Email,
			Password: string(hashedPass),
		}

		tx := ctx.DBConn.Session(&gorm.Session{Context: r.Context()})

		result := tx.Create(&user)
		if result.Error != nil {
			// Check for duplicate username error
			// Note: Error handling varies slightly by DB driver, generic check here:
			ctx.Logger.Error("db error", zap.Error(result.Error))
			http.Error(w, "Username likely taken or database error", http.StatusConflict)
			return
		}

		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("User registered successfully"))
	}
}

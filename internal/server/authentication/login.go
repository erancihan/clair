package authentication

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/a-h/templ"
	"github.com/erancihan/clair/internal/database/models"
	server_context "github.com/erancihan/clair/internal/server/context"
	"github.com/erancihan/clair/internal/web"
	"github.com/erancihan/clair/internal/web/pages"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// LoginPage renders the login form. It threads a validated `next` return path
// from the query string into a hidden form field so a browser that was redirected
// here by AuthMiddleware returns to where it started after signing in.
func LoginPage(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next := safeNext(r.URL.Query().Get("next"), "")
		templ.Handler(web.Base(PageShell(w, r, "Clair"), pages.LoginPage(next))).ServeHTTP(w, r)
	}
}

type LoginPayload struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// AuthLogin authenticates a user and establishes a session. It preserves the
// existing JSON contract exactly — a JSON request (Content-Type: application/json)
// still receives a bare 200 with the session cookie set — while additionally
// supporting HTML form submissions, which are redirected to a validated `next`
// path (falling back to /dashboard).
func AuthLogin(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		isJSON := strings.Contains(r.Header.Get("Content-Type"), "application/json")

		var creds LoginPayload
		var next string

		if isJSON {
			if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
				http.Error(w, "Invalid request payload", http.StatusBadRequest)
				return
			}
		} else {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "Invalid request payload", http.StatusBadRequest)
				return
			}
			creds.Email = r.PostFormValue("email")
			creds.Password = r.PostFormValue("password")
			next = r.PostFormValue("next")
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

		// Rotate the CSRF token across this privilege change. The pre-login token
		// would otherwise carry into the authenticated session, so anyone who had
		// planted or observed the visitor's earlier session cookie would hold a
		// valid token for it. Dropping it makes the next protected request mint a
		// fresh one.
		delete(session.Values, csrfSessionField)

		session.Options = sessionCookieOptions()

		session.Save(r, w)

		// if the request is from API, return 200 OK (unchanged contract)
		if isJSON {
			w.WriteHeader(http.StatusOK)
			return
		}

		// else, redirect to the validated return path (or the dashboard)
		http.Redirect(w, r, safeNext(next, "/dashboard"), http.StatusFound)
	}
}

// safeNext validates a post-login return path so it can only point back into this
// application. It accepts same-origin absolute paths (a single leading slash) and
// rejects everything else — empty values, protocol-relative "//host" URLs,
// backslash tricks, and fully-qualified URLs — falling back to def.
func safeNext(next, def string) string {
	if next == "" {
		return def
	}

	// Must be an absolute path, and must not be protocol-relative ("//host") or a
	// backslash variant that some browsers normalise to "//".
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return def
	}

	// Reject anything that parses as (or carries) an absolute URL with a host.
	u, err := url.Parse(next)
	if err != nil || u.IsAbs() || u.Host != "" {
		return def
	}

	return next
}

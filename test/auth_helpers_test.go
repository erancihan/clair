package test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/erancihan/clair/internal/database"
	"github.com/erancihan/clair/internal/database/models"
	authentication "github.com/erancihan/clair/internal/server/authentication"
	server_context "github.com/erancihan/clair/internal/server/context"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// newAuthTestDB opens a gorm connection to the test Postgres database and resets
// the whole app schema (drop + AutoMigrate over the migration set) so each auth
// test starts from a clean schema that includes the Role column.
func newAuthTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = defaultTestDatabaseURL
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to connect to test database at %s: %v", dsn, err)
	}

	dropAppTables(t, db)

	if err := db.AutoMigrate(database.MigrationModels()...); err != nil {
		t.Fatalf("failed to migrate the test schema: %v", err)
	}

	return db
}

// newAuthTestContext builds a BackEndContext backed by the test database and a
// no-op logger, suitable for constructing the authentication middleware/handlers
// directly in a test.
func newAuthTestContext(t *testing.T) server_context.BackEndContext {
	t.Helper()
	return server_context.BackEndContext{
		DBConn: newAuthTestDB(t),
		Logger: zap.NewNop(),
	}
}

// createUser inserts a user with the given email, password (bcrypt-hashed) and
// role, returning the created row. An empty role lets the database default apply.
func createUser(t *testing.T, db *gorm.DB, email, password, role string) models.User {
	t.Helper()

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}

	user := models.User{Email: email, Password: string(hash), Role: role}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	return user
}

// authTestServer bundles an httptest.Server that mounts the authentication layer
// (login + a few guarded/echo routes) against a shared context, along with the
// database handle so tests can manipulate rows directly.
type authTestServer struct {
	*httptest.Server
	ctx server_context.BackEndContext
	db  *gorm.DB
}

// newAuthTestServer wires up a self-contained httptest server exercising the auth
// layer:
//
//	POST /login   -> AuthLogin (JSON + form)
//	GET  /whoami  -> behind AuthMiddleware; echoes CurrentUser as JSON
//	GET  /admin   -> behind AdminMiddleware; echoes "ok"
//	GET  /owner   -> echoes OwnerRef (guest identity path)
//	GET  /owner-auth -> behind AuthMiddleware; echoes OwnerRef (user path)
func newAuthTestServer(t *testing.T) *authTestServer {
	t.Helper()

	ctx := newAuthTestContext(t)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", authentication.AuthLogin(ctx))
	mux.HandleFunc("GET /login-page", authentication.LoginPage(ctx))

	mux.Handle("GET /whoami", authentication.AuthMiddleware(ctx)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			identity, _ := authentication.CurrentUser(r.Context())
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user_id": identity.UserID,
				"role":    identity.Role,
			})
		})))

	mux.Handle("GET /admin", authentication.AdminMiddleware(ctx)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})))

	mux.HandleFunc("GET /owner", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(authentication.OwnerRef(w, r)))
	})

	// Shares the session store with /login, so tests can observe how the CSRF
	// token behaves across an authentication boundary.
	mux.Handle("GET /csrf", authentication.CSRF()(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(authentication.CSRFToken(r)))
		})))

	mux.Handle("GET /owner-auth", authentication.AuthMiddleware(ctx)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(authentication.OwnerRef(w, r)))
		})))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &authTestServer{Server: srv, ctx: ctx, db: ctx.DBConn}
}

// loginJSON performs a JSON login against the test server and returns the session
// cookies set on success.
func loginJSON(t *testing.T, srv *authTestServer, email, password string) []*http.Cookie {
	t.Helper()

	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	// Do not follow redirects; capture the raw response.
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected login 200, got %d", resp.StatusCode)
	}

	cookies := resp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("login did not set any cookies")
	}
	return cookies
}

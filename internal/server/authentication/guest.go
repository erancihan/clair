package authentication

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	server_context "github.com/erancihan/clair/internal/server/context"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// GuestMigrator hands work a visitor accumulated while anonymous over to the
// account they just signed into.
//
// It receives the visitor's pre-login guest owner reference ("guest:<sid>") and
// the reference of the account they signed into ("user:<id>") — the exact
// strings OwnerRef produces and domains persist — so a domain can re-point its
// own rows without knowing anything about sessions or cookies. ctx is the login
// request's context; db is the application's connection.
//
// A migrator must be safe to run more than once for the same pair of refs: a
// visitor can sign in repeatedly from the same browser, and the guest reference
// outlives the login (the "sid" cookie is deliberately not cleared).
type GuestMigrator func(ctx context.Context, db *gorm.DB, guestRef, userRef string) error

// registeredMigrator pairs a migrator with the name it is identified by in logs.
// Without the name, a failure in one domain's migrator is indistinguishable from
// another's — which is the whole of what an operator needs to know.
type registeredMigrator struct {
	name    string
	migrate GuestMigrator
}

var (
	guestMigratorsMu sync.RWMutex
	guestMigrators   []registeredMigrator
)

// RegisterGuestMigrator registers m to run on every successful login, under name
// for logging.
//
// Domains call this from their own package — typically from Mount — so this
// layer never imports them: the dependency runs domains -> authentication and
// the registry is what inverts it. Registering nothing is the normal case and
// costs nothing.
//
// Registration is expected during startup, but the registry is safe for
// concurrent use regardless. A nil migrator is ignored.
func RegisterGuestMigrator(name string, m GuestMigrator) {
	if m == nil {
		return
	}

	guestMigratorsMu.Lock()
	defer guestMigratorsMu.Unlock()

	guestMigrators = append(guestMigrators, registeredMigrator{name: name, migrate: m})
}

// runGuestMigrators invokes every registered migrator for a visitor who has just
// signed in.
//
// Nothing here may cost the visitor their login. A migrator that returns an
// error — or panics — is logged and skipped, and the remaining migrators still
// run: one domain failing to move a cart must not also strand another domain's
// booking. This is a best-effort, fire-and-log hand-over, not a transaction.
//
// It is a no-op when nothing is registered, and when the visitor arrived with no
// guest session at all: with no "sid" cookie there is no guest identity anything
// could have been recorded against.
func runGuestMigrators(ctx server_context.BackEndContext, r *http.Request, guestRef, userRef string) {
	if guestRef == "" {
		return
	}

	guestMigratorsMu.RLock()
	migrators := make([]registeredMigrator, len(guestMigrators))
	copy(migrators, guestMigrators)
	guestMigratorsMu.RUnlock()

	for _, migrator := range migrators {
		runGuestMigrator(ctx, r.Context(), migrator, guestRef, userRef)
	}
}

// runGuestMigrator runs a single migrator, containing both its errors and its
// panics so neither escapes into the login handler.
func runGuestMigrator(
	ctx server_context.BackEndContext,
	reqCtx context.Context,
	migrator registeredMigrator,
	guestRef, userRef string,
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logGuestMigratorFailure(ctx, migrator.name, fmt.Errorf("panic: %v", recovered))
		}
	}()

	if err := migrator.migrate(reqCtx, ctx.DBConn, guestRef, userRef); err != nil {
		logGuestMigratorFailure(ctx, migrator.name, err)
	}
}

// logGuestMigratorFailure records a migrator failure. The login itself already
// succeeded by this point, so this log is the only trace the hand-over did not.
func logGuestMigratorFailure(ctx server_context.BackEndContext, name string, err error) {
	if ctx.Logger == nil {
		return
	}

	ctx.Logger.Error("guest migrator failed",
		zap.String("migrator", name),
		zap.Error(err),
	)
}

// preLoginGuestRef returns the owner reference the caller was using as a guest,
// or "" when they arrived without a guest session.
//
// It reads the "sid" cookie directly rather than going through SessionID, which
// would mint one: a visitor with no guest session has nothing to migrate, and
// minting here would set a cookie on the login response for no reason.
func preLoginGuestRef(r *http.Request) string {
	cookie, err := r.Cookie(sessionIDCookieName)
	if err != nil || cookie.Value == "" {
		return ""
	}

	return guestRef(cookie.Value)
}

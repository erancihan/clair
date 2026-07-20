<!-- Unified authentication, identity, session & app-security design for clair.
     Consolidates the auth requirements scattered across the shop plan (§7) and the
     booking design (§2), which both depend on ONE shared auth layer that does not
     exist yet. This document is the single authority for that layer. -->

# Clair Authentication & Identity — System Design

**Audience:** implementing engineer · **Module:** `github.com/erancihan/clair` · **Package:** `internal/server/authentication` · **Stack:** Go 1.25 / GORM (PostgreSQL) / `gorilla/sessions`

---

## Table of contents

1. [Verdict & scope](#1-verdict--scope)
2. [Current state & gaps](#2-current-state--gaps)
3. [Requirements from the two domains](#3-requirements-from-the-two-domains)
4. [Identity model](#4-identity-model)
5. [Sessions & hardening](#5-sessions--hardening)
6. [Anonymous / guest identity (`sid` + `OwnerRef`)](#6-anonymous--guest-identity)
7. [Authentication flows](#7-authentication-flows)
8. [Authorization (middleware)](#8-authorization)
9. [CSRF](#9-csrf)
10. [Webhook / machine authentication](#10-webhook--machine-authentication)
11. [Token generation](#11-token-generation)
12. [Route wiring](#12-route-wiring)
13. [Consumers — who uses what](#13-consumers)
14. [Package layout](#14-package-layout)
15. [Migration plan (non-breaking)](#15-migration-plan)
16. [Test plan](#16-test-plan)
17. [Key invariants](#17-key-invariants)
18. [Open decisions](#18-open-decisions)
19. [Known limitations / deferred](#19-known-limitations)

---

## 1. Verdict & scope

**One shared auth layer, not a new service.** Authentication is a cross-cutting concern that the
shop CMS, the booking domain, and any future admin surface all depend on. Both existing plans say so
explicitly — the shop plan §7 specs the target; the booking design §2 says *"Auth & roles, built
once … do not stand up a second role system … build one anonymous-session helper plus a
`CurrentUser(ctx)` accessor, shared by both."* This document consolidates those into the single
authority for the layer and **supersedes** shop-plan §7 and the booking design's auth notes (those
should link here).

Scope: identity, sessions, login/register/logout, role-based authorization, anonymous/guest identity,
CSRF, webhook signature auth, and token generation — all in `internal/server/authentication`
(imported as `api_auth` in `server.go`). It is a **hardening + extension of the existing
gorilla-sessions auth**, not a rewrite: existing `/api/v1/auth/*` routes keep working.

Out of scope (deferred, §19): OAuth/SSO, MFA, password reset e-mail flow, API keys, per-jurisdiction
PII retention.

---

## 2. Current state & gaps

What exists on `master` today (`internal/server/authentication/`):

| File | Provides |
|---|---|
| `constant.go` | `SESSION_NAME`; a `gorilla/sessions.CookieStore` with a **hard-coded** key |
| `login.go` | `LoginPage` (renders form, ignores request); `AuthLogin` (JSON creds → bcrypt check → session; redirects to `/dashboard`) |
| `register.go` | `AuthRegister` (JSON → bcrypt → `users` row) |
| `logout.go` | `AuthLogout` (expires the session cookie) |
| `middleware.go` | `AuthMiddleware` (validates session, re-checks the user row exists) |

**Gaps this design closes** (the middleware's own `TODO` says *"enhance middleware to support
roles/permissions"*):

1. **No roles.** `User` has no `Role`; nothing gates an admin surface.
2. **No request-context identity.** `AuthMiddleware` validates but injects nothing, so no handler can
   cheaply learn *who* the caller is. Booking's `OwnerRef` and the shop CMS both need this.
3. **No anonymous/guest identity.** No stable id for anonymous carts (shop) or guest holds (booking).
4. **Hard-coded session key**, `Secure:false` cookies — unsafe for an authed surface.
5. **No CSRF** on state-changing routes.
6. **No webhook auth** for machine callers (booking's payment provider).
7. **Failure mode is a flat 401** (bad UX for a browser CMS; no `/login` redirect, no `next`).
8. **`LoginPage`/`AuthLogin` ignore a return path** and always land on `/dashboard`.

---

## 3. Requirements from the two domains

Synthesized from shop-plan §7 / §2.1 and booking-design §2 / §10:

| Requirement | Shop | Booking |
|---|---|---|
| Roles + `AdminMiddleware` gating `/…/admin/*` | CMS at `/shop/admin/*` | `/admin/*` (event cancel, refunds, seat block) — **reuses** it |
| `CurrentUser(ctx)` request-context identity | authed handlers | `OwnerRef` + fulfilment |
| Anonymous/guest identity | cart owner (`sid`) | guest hold/order `OwnerRef` — *its only "who is this"* |
| CSRF on mutations | add-to-cart, checkout, order-status | `/hold` · `/checkout` · `/cancel` |
| Webhook signature auth | — | `/webhooks/payments/{provider}` (CSRF-exempt) |
| Guest checkout (nullable `UserID`) | `ShopOrder.UserID` | `BookingAppointment.UserID`, `BookingOrder.OwnerRef` |
| Session hardening (env key, `Secure`) | admin CMS | admin + money flows |
| High-entropy tokens | order number, `sid`, CSRF | hold/cancel tokens, `sid` |

Both domains share **one** `User`, one `Role`, one `AdminMiddleware`, one `CurrentUser`/`sid`.

---

## 4. Identity model

`User` is the single shared model (bare `users` table). It gains a `Role` — a plain string so
`vendor`/`staff` can be added later without a migration.

```go
// internal/database/models/users.go
type User struct {
	gorm.Model
	ID       uint   `json:"id" gorm:"primaryKey"`
	Username string `json:"username"`
	Email    string `json:"email" gorm:"uniqueIndex"`
	Password string `json:"password"`
	Role     string `json:"role" gorm:"index;default:customer"`
}
```

```go
// internal/server/authentication/roles.go
const (
	RoleCustomer = "customer"
	RoleAdmin    = "admin"
	// headroom: "vendor", "staff", … — no schema change needed
)
```

`AuthMiddleware` puts an `Identity` in the request context; `CurrentUser` reads it. This is the
**load-bearing gap** both designs called out.

```go
// internal/server/authentication/identity.go
type ctxKey int

const identityKey ctxKey = iota

type Identity struct {
	UserID uint
	Role   string
}

func withIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

// CurrentUser returns the authenticated identity; ok == false for anonymous requests.
func CurrentUser(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey).(Identity)
	return id, ok
}
```

---

## 5. Sessions & hardening

Keep `gorilla/sessions` (already a dependency), but source the signing key from the environment and
**fail closed** in production; make cookies `Secure` behind TLS.

```go
// internal/server/authentication/constant.go
const SESSION_NAME = "session-name"

var store = sessions.NewCookieStore(sessionKey())

func sessionKey() []byte {
	if k := os.Getenv("SESSION_KEY"); k != "" {
		return []byte(k)
	}
	if os.Getenv("APP_ENV") == "production" {
		panic("SESSION_KEY must be set in production") // fail closed
	}
	return []byte("dev-only-insecure-key-change-me") // dev fallback only
}

// SecureCookies reports whether cookies should set the Secure flag (prod/TLS).
func SecureCookies() bool { return os.Getenv("APP_ENV") == "production" }
```

Session cookie options (set on login and on the `sid`): `HttpOnly`, `SameSite=Lax`,
`Secure: SecureCookies()`. The **authenticated session** cookie (`session-name`) is short-lived
(`MaxAge` ~ 1h today; consider a sliding refresh, §18). The **`sid` guest cookie** (§6) is long-lived
(90d) and orthogonal — a user can be anonymous (`sid` only) or authenticated (`sid` + session).

---

## 6. Anonymous / guest identity

The shop's cart owner and booking's guest hold owner must be the **same** anonymous id. One `sid`
cookie scoped to `/` (so every domain sees it); `OwnerRef` is the single answer to *"who owns this
cart / hold / order."*

```go
// internal/server/authentication/session.go
const SessionCookie = "sid" // shared guest/session id

// SessionID returns a stable per-browser id, minting an HttpOnly cookie on first use.
func SessionID(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		return c.Value
	}
	id := SecureToken()
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: id, Path: "/", // "/" — booking reads it too
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: 90 * 24 * 3600, Secure: SecureCookies(),
	})
	return id
}

// OwnerRef owns carts, holds, and orders: "user:<id>" when authenticated, else "guest:<sid>".
// This is the CANONICAL on-disk format; booking adopts it (its "sess:abc" examples are superseded).
func OwnerRef(w http.ResponseWriter, r *http.Request) string {
	if id, ok := CurrentUser(r.Context()); ok {
		return fmt.Sprintf("user:%d", id.UserID)
	}
	return "guest:" + SessionID(w, r)
}
```

> **⚠ Format decision (§18).** Booking's `BookingHold.OwnerRef` / `BookingOrder.OwnerRef` persist this
> string. Canonical is `user:<id>` / `guest:<sid>`. Booking's design doc currently shows `sess:abc`;
> **lock the format before booking writes those rows** — the `user:`/`guest:` prefix encodes auth
> state, which a bare session id does not.
>
> **Login migration (nice-to-have):** on login, re-own the `guest:<sid>` cart/holds to `user:<id>` so
> a buyer doesn't lose an in-progress cart.

---

## 7. Authentication flows

Keep the existing bcrypt + `gorilla/sessions` flow; harden three things.

**Register** — unchanged in shape (bcrypt `DefaultCost`, unique-email conflict → 409). Add basic
password policy (min length) and normalize e-mail (lower-case/trim). New users default to
`RoleCustomer`.

**Login** — validate creds, create session, then **honor a validated `next`**:

```go
// after a successful bcrypt check and session.Save(...)
if r.Header.Get("Content-Type") == "application/json" {
	w.WriteHeader(http.StatusOK)
	return
}
http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusFound)

// safeNext rejects open-redirects: only same-origin, leading-slash, non-"//" paths.
func safeNext(next string) string {
	if strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") {
		return next
	}
	return "/dashboard"
}
```

`LoginPage` must render the `next` query param into a hidden form field so it round-trips.

**Logout** — expires the session cookie (unchanged). Leave the `sid` cookie intact (the guest
identity survives logout; only the authenticated session ends).

---

## 8. Authorization

`AuthMiddleware` validates the session **and injects `Identity`**; failure is **content-negotiated**
(browsers → `/login`, API → 401). `AdminMiddleware` layers a role gate.

```go
// internal/server/authentication/middleware.go
func AuthMiddleware(ctx server_context.BackEndContext) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			session, err := store.Get(r, SESSION_NAME)
			if err != nil {
				unauthorized(w, r)
				return
			}
			auth, _ := session.Values["authenticated"].(bool)
			userID, _ := session.Values["id"].(uint)
			if !auth || userID == 0 {
				unauthorized(w, r)
				return
			}

			var user models.User
			tx := ctx.DBConn.Session(&gorm.Session{Context: r.Context()})
			tx.Limit(1).Where("id = ?", userID).Find(&user)
			if user.ID == 0 { // user deleted since login
				unauthorized(w, r)
				return
			}

			r = r.WithContext(withIdentity(r.Context(), Identity{UserID: user.ID, Role: user.Role}))
			next.ServeHTTP(w, r)
		})
	}
}

// AdminMiddleware wraps AuthMiddleware (identity already injected) and gates on role.
// Booking's /admin/* routes reuse this verbatim.
func AdminMiddleware(ctx server_context.BackEndContext) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return AuthMiddleware(ctx)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := CurrentUser(r.Context())
			if !ok || id.Role != RoleAdmin {
				http.Error(w, "Forbidden", http.StatusForbidden) // 403, no redirect loop
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

// unauthorized: browsers → redirect to /login (return path); API/JSON → 401.
func unauthorized(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") ||
		r.Header.Get("Content-Type") == "application/json" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.Path), http.StatusFound)
}
```

**Failure matrix** (asserted by every protected route):

| Caller | State | Result |
|---|---|---|
| Browser (`Accept: text/html`) | unauthenticated | 302 → `/login?next=…` |
| API (`Accept/Content-Type: application/json`) | unauthenticated | 401 |
| Any | authenticated, non-admin, admin route | 403 |
| Any | valid cookie, user row deleted | re-check fails → 302/401 |

---

## 9. CSRF

One synchronizer token bound to the session, verified by **one** middleware on every unsafe method,
across shop **and** booking mutating routes. The **webhook is exempt** (machine caller, provider
signature, no browser session — §10).

```go
// internal/server/authentication/csrf.go
func CSRFToken(r *http.Request) (string, error) { // read-or-create; render into a hidden field
	sess, _ := store.Get(r, SESSION_NAME)
	if t, ok := sess.Values["csrf"].(string); ok && t != "" {
		return t, nil
	}
	sess.Values["csrf"] = SecureToken()
	return sess.Values["csrf"].(string), nil // caller must sess.Save on the response
}

func CSRF() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				sess, _ := store.Get(r, SESSION_NAME)
				want, _ := sess.Values["csrf"].(string)
				got := r.Header.Get("X-CSRF-Token")
				if got == "" {
					got = r.PostFormValue("csrf_token")
				}
				if want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
					http.Error(w, "bad CSRF token", http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
```

Forms carry `<input type="hidden" name="csrf_token" value={ token }>`; AJAX sends `X-CSRF-Token`.

---

## 10. Webhook / machine authentication

The booking payment webhook (`POST /webhooks/payments/{provider}`) is authenticated by the provider's
**signature over the raw request body**, not a browser session — so it is mounted **outside** the CSRF
and session middleware, reads the raw body (never `ParseForm`), and verifies with a constant-time
compare.

```go
// internal/server/authentication/webhook.go
// VerifyProviderSignature checks an HMAC-SHA256 signature over the raw body.
func VerifyProviderSignature(secret string, body []byte, sigHeader string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(sigHeader))
}
```

The webhook handler (booking-owned) reads `body, _ := io.ReadAll(r.Body)` first, verifies, then
processes idempotently (dedupe on `BookingPayment.IdempotencyKey`). Replay/idempotency and the
capture-time commit live in the booking kernel; **only signature verification is shared here**.

---

## 11. Token generation

One high-entropy helper for every unguessable value (`sid`, CSRF token, order numbers, hold/cancel
tokens). Do **not** reuse `utils.GenerateGameID` (an ~8-char, ~47-bit game id) for security tokens.

```go
// internal/server/authentication/session.go
// SecureToken returns a URL-safe 256-bit random token.
func SecureToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand
	return base64.RawURLEncoding.EncodeToString(b)
}
```

---

## 12. Route wiring

Middleware is mounted per group in `server.Routes()` (the custom `router.Router`'s `Group` takes
trailing middleware; `Middleware(...).Group(...)` chains). Safe methods pass through CSRF untouched.

```
/                         public                (no auth)
/login /logout            public                (session issue/clear)
/api/v1/auth/*            public                (login/register/logout)
/shop/*                   public + CSRF         (storefront + cart + checkout)
  /shop/admin/*           + AdminMiddleware     (CMS)
/appointments/* /events/* public + CSRF         (booking storefront)
/admin/*                  + AdminMiddleware      (booking admin)  ← reuses the SAME middleware
/webhooks/*               signature only         (NO session, NO CSRF)
```

```go
// example: shop group with app-wide CSRF; admin subgroup adds the role gate
mux.Group("shop", func(shop *router.Router) {
	// … storefront + cart + checkout …
	shop.Middleware(api_auth.AdminMiddleware(s.context)).Group("admin", func(admin *router.Router) {
		// … CMS …
	})
}, api_auth.CSRF())

// webhooks: no CSRF, no session — signature verified inside the handler
mux.Group("webhooks", func(wh *router.Router) {
	wh.HandleFunc("POST /payments/{provider}", bookingWebhook(s.context))
})
```

---

## 13. Consumers

| Piece | Shop uses it for | Booking uses it for |
|---|---|---|
| `CurrentUser(ctx)` | admin identity, order ownership | fulfilment, admin |
| `AdminMiddleware` | `/shop/admin/*` | `/admin/*` |
| `SessionID` / `OwnerRef` | cart key, `ShopOrder` owner | `BookingHold`/`BookingOrder` owner |
| `CSRF()` | cart/checkout/CMS mutations | `/hold` · `/checkout` · `/cancel` |
| `VerifyProviderSignature` | (future payments) | payment webhook |
| `SecureToken` | order number, `sid`, CSRF | hold/cancel tokens, `sid`, CSRF |
| `RoleCustomer`/`RoleAdmin` | CMS gate | admin gate |

---

## 14. Package layout

```
internal/server/authentication/
  constant.go     # SESSION_NAME, store, sessionKey (env), SecureCookies
  roles.go        # RoleCustomer / RoleAdmin
  identity.go     # Identity, ctx key, withIdentity, CurrentUser
  session.go      # sid cookie, SessionID, OwnerRef, SecureToken
  middleware.go   # AuthMiddleware (inject), AdminMiddleware, unauthorized
  csrf.go         # CSRFToken, CSRF middleware
  webhook.go      # VerifyProviderSignature (HMAC)
  login.go        # LoginPage (renders next), AuthLogin (safeNext, Secure cookie)
  register.go     # AuthRegister (policy, default RoleCustomer)
  logout.go       # AuthLogout (keeps sid)
```

Everything stays in **one package** so both domains consume it via `api_auth` without importing
`shop` or `booking` (no import cycles).

---

## 15. Migration plan

Non-breaking, incremental — existing `/api/v1/auth/*` behavior is preserved; each step is
independently shippable. **Build order: this layer is built first** (booking's Phase 0 needs it before
its holds/orders can carry an `OwnerRef`; the shop's Phase 0 then just consumes it).

1. **Identity + roles** — add `User.Role`; `identity.go` + inject in `AuthMiddleware`; `CurrentUser`.
   Existing routes keep passing; add the role column via `AutoMigrate`.
2. **Session hardening** — `sessionKey()` (env, fail-closed), `SecureCookies()`, set `Secure` on
   login + `sid`.
3. **Guest identity** — `session.go` (`sid`, `SessionID`, `OwnerRef`, `SecureToken`).
4. **Authorization UX** — `AdminMiddleware`, content-negotiated `unauthorized`, `safeNext` + `next`
   round-trip in `LoginPage`/`AuthLogin`.
5. **CSRF** — `csrf.go`; mount `CSRF()` on the shop + booking mutating groups; render tokens in forms.
6. **Webhook auth** — `VerifyProviderSignature`; booking mounts `/webhooks/*` outside CSRF.

**Acceptance criteria** (handed to whoever builds it — booking first): helpers live in
`internal/server/authentication`; `OwnerRef` uses the canonical `user:`/`guest:` format; `User.Role`
uses `RoleCustomer`/`RoleAdmin`; CSRF is app-wide (covers shop routes too); the webhook path is
CSRF-exempt; nothing is duplicated in `internal/booking`.

---

## 16. Test plan

| # | Layer | Target | Assertion |
|---|---|---|---|
| 1 | U | `SecureToken` | 256-bit, URL-safe, unique across calls |
| 2 | U | `OwnerRef` | `user:<id>` when authed, `guest:<sid>` otherwise |
| 3 | U | `safeNext` | same-origin `/x` kept; `//host`, `http://…` → `/dashboard` |
| 4 | I | login → `CurrentUser` | after login, a downstream handler sees the right `Identity` |
| 5 | I | `AdminMiddleware` | admin → pass; customer → 403; unauth browser → 302 `/login`; unauth JSON → 401 |
| 6 | I | user deleted mid-session | valid cookie, no row → unauthorized |
| 7 | I | CSRF | tokenless POST → 403; matching token → pass; GET → pass |
| 8 | I | CSRF exemption | webhook POST with no CSRF token → not blocked by CSRF |
| 9 | I | `sid` | first request mints `Set-Cookie sid` (HttpOnly, `Path=/`); reused thereafter |
| 10 | I | webhook signature | valid HMAC → accepted; tampered body/sig → rejected |
| 11 | I | session hardening | `SESSION_KEY` unset in `APP_ENV=production` → startup panics |

---

## 17. Key invariants

1. **One** `User`, one `Role`, one `AdminMiddleware`, one `CurrentUser`, one `sid`/`OwnerRef` — no
   second scheme in any domain.
2. `AuthMiddleware` injects identity **before** `AdminMiddleware` reads it (composition order).
3. `OwnerRef` persisted format is `user:<id>` / `guest:<sid>` everywhere.
4. Security tokens (`sid`, CSRF, order/hold numbers) come from `SecureToken` (256-bit), never the
   game-id generator.
5. CSRF guards every browser-driven mutation; the webhook (machine, signed) is the only exemption.
6. The session signing key is env-sourced and **fails closed** in production.
7. Auth helpers live only in `internal/server/authentication`.

---

## 18. Open decisions

- **`OwnerRef` on-disk format** — lock `user:`/`guest:` before booking writes `BookingHold`/
  `BookingOrder` (recommended; supersedes the booking doc's `sess:abc`).
- **Session lifetime / refresh** — current authed session is a fixed 1h `MaxAge`. Sliding refresh
  (re-issue on activity) vs. fixed expiry? (Recommend sliding, capped.)
- **Password policy** — minimum length / breach check? (Recommend a min length now; HIBP later.)
- **Login rate-limiting** — Valkey token bucket on `/api/v1/auth/login` to slow credential stuffing
  (Valkey is optional/fail-open — degrade to no-limit if down).

## 19. Known limitations

- **No OAuth/SSO, no MFA** — deferred; the session model is username/password only.
- **No password-reset e-mail flow** — deferred.
- **No API keys / service accounts** — machine auth is limited to the provider-signed webhook.
- **PII retention / right-to-erasure** — deferred (booking's gocron retention job handles guest PII
  purging on its side).
- **CSRF strategy is synchronizer-token** (session-bound); a stateless double-submit variant is not
  used because the session already exists.

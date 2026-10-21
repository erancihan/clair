# Shop Front System — Implementation Plan

Status: **Phase 0 (seams) written, not yet verified against Postgres** (2026-10-20). Merge order and rebase notes: `docs/HANDOFF.md` on `feat/booking-systems`.
Branch: `docs/shop-front-plan` (rebased on `master` at `a1b3215`, 2026-09-02)

This revision re-grounds the plan on what has landed on `master` since it was first written.
Section 14 lists every change from the previous revision and why. Read it first if you reviewed
the earlier text.

## 0. Locked decisions

| Decision | Choice |
|---|---|
| Storefront base | `/shop` (canonical, no trailing slash — §4.3 explains why) |
| CMS base | `/shop/admin` (`AdminMiddleware` + `CSRF()`) |
| Tenancy | Single store, one owner. Multi-vendor is a later additive layer. |
| Payments | Mock/manual now. Orders are created `pending_payment`; a `PaymentProvider` seam (§3.6) is the only thing Stripe later replaces. |
| Relationship to the booking kernel | **Independent.** The shop keeps its own `shop_*` tables for orders, stock and payment. It does not touch `BookingInventory`, holds, `BookingOrder`, the payment webhook or the fulfiller registry. (Reuse was considered and rejected 2026-09-02: the user wants the shop as a self-contained domain with zero kernel changes.) |
| Model location | `internal/database/models/shop_*.go`, one file per model, `Shop*` type prefix — the convention `database.go` states and booking follows. No separate `sm` package. |
| Route wiring | `internal/server/routes/shop.go` owns paths and guards; `internal/server/shop` exposes an `API` struct with one method per handler, files named `api_*.go`. This is the shape the booking domain is moving to in the parallel work. If `internal/server/routes/` has not landed when the shop's Phase 0 starts, the fallback is a games-style `shop.Mount(r, ctx)` in `internal/server/shop/mount.go` (fallback only; not in the §1.1 tree), and the route table moves to `routes/shop.go` in the same PR that lands the package. |
| Rendering | Server-rendered `templ` pages, plain `POST` forms, Post/Redirect/Get with `303`. Alpine.js only for nice-to-haves (cart badge, quantity stepper). |
| Not-found | Real `404` status with the `pages.NotFound()` body — for unknown products/categories/orders **and** for any unmatched path or wrong verb under `/shop/`, via a shop-owned subtree fallback (§4.3). The site's own catch-all `/` answers `200` for unknown top-level paths (`server.go`, pinned by `test/mount_seam_test.go`); without the shop fallback every unmatched `/shop/*` request, and every wrong-verb request, would land there. |
| Identity | Consumed from `internal/server/authentication`. Cart and order ownership is `api_auth.OwnerRef` from day one; `OptionalAuthMiddleware` already runs in front of every application route. |
| Guest cart on login | Merge via `api_auth.RegisterGuestMigrator("shop", …)`. |
| Currency | Single-currency USD at launch. Every row still carries `Currency` + minor units. |
| Order number | `api_auth.SecureToken()` (256-bit, URL-safe). It is the guest's only guard on `/shop/orders/{number}`. |

---

## 1. How it fits `master`

`master` has settled a set of seams every domain is expected to use. The shop plugs into each of
them with one line and otherwise lives in its own packages.

| Seam | Where on `master` | Shop's one line |
|---|---|---|
| Route table | `server.Routes()` mounts one line per domain (`games.Mount`, `routes.MountBooking` in the parallel work) | `routes.MountShop(app, s.context)` |
| Migration set | `database.MigrationModels()` appends one `Models()` per domain | `all = append(all, shop.Models()...)` |
| Navigation | `components.NavLinks` slice in `internal/web/components/nav.go` | `{Href: "/shop", Label: "Shop"}` |
| Guest hand-over | `api_auth.RegisterGuestMigrator(name, fn)` called from the domain's constructor | `api_auth.RegisterGuestMigrator("shop", …)` in `shop.NewAPI` |
| Seed data | `clair seed` (`internal/cmd/seed.go`) — **parallel work only, not on `master`**; it seeds the Aurenor catalog | one call to `shop.Seed(db)` from the same command once it lands; until then a `clair seed-shop` subcommand (§11, Phase 2) |
| Identity everywhere | `OptionalAuthMiddleware` on `app` in `server.Routes()` | nothing — `CurrentUser`/`OwnerRef` already work on public routes |

All four one-liners touch files the parallel booking work is also editing (`server.go`,
`database.go`, `nav.go`, `seed.go`). They are trivial to re-apply after a rebase, but they are
the whole of the shop's conflict surface, so Phase 0 (§11) lands them first and small.

> **Two `master` defects Phase 0 has to clear first.** (1) `master` at `a1b3215` does not
> build: `internal/cmd/scheduler.go:31` calls `clair.NewEventLoop`, which no longer exists
> (`internal/clair/` holds only `sentry.go`); `test/utils.go` imports `internal/cmd`, so the
> whole `test` package is unbuildable. (2) `MigrateCmd` exists (`internal/cmd/migrate.go`) but
> `internal/cmd/root.go` never calls `AddCommand(MigrateCmd(ctx))`, so `clair migrate` is not a
> runnable command on `master`. The parallel tree touches both files (`scheduler.go`, `root.go`
> — it registers `MigrateCmd` and `SeedCmd`); if it lands first, both are fixed for free. If
> not, Phase 0 fixes them: they are the reason nothing else in this plan can be verified.

### 1.1 Package layout

```
internal/
├── database/models/
│   ├── shop_category.go        # ShopCategory       -> shop_categories
│   ├── shop_product.go         # ShopProduct        -> shop_products
│   ├── shop_product_image.go   # ShopProductImage   -> shop_product_images
│   ├── shop_cart.go            # ShopCart           -> shop_carts   (DB cart store)
│   ├── shop_order.go           # ShopOrder          -> shop_orders
│   └── shop_order_item.go      # ShopOrderItem      -> shop_order_items
├── shop/                       # domain: pure logic, no net/http
│   ├── errors.go               #   ErrEmptyCart, ErrOutOfStock, ErrUnavailable, ErrBadTransition
│   ├── money.go                #   FormatCents
│   ├── slug.go                 #   Slugify + UniqueSlug (collision suffixing against the DB)
│   ├── catalog.go              #   Catalog: public queries + admin CRUD
│   ├── cart.go                 #   Cart, CartItem, CartStore interface, NewCartStore
│   ├── cart_valkey.go          #   Valkey-backed CartStore
│   ├── cart_db.go              #   shop_carts-backed CartStore
│   ├── checkout.go             #   PlaceOrder: cart -> ShopOrder in one transaction
│   ├── order_status.go         #   allowed-transitions map + Transition
│   ├── payment.go              #   PaymentProvider interface + ManualProvider
│   ├── guest.go                #   MigrateGuest: merge carts, re-point pending orders
│   └── seed.go                 #   Seed(db): idempotent demo catalog
├── server/shop/                # HTTP layer
│   ├── api.go                  #   API struct, NewAPI, one method per handler
│   ├── api_http.go             #   db(), page(), notFound(), parseID(), formInt()
│   ├── api_storefront.go       #   landing, product list, product detail, category
│   ├── api_cart.go             #   view, add, update, remove
│   ├── api_checkout.go         #   checkout form, submit, order confirmation
│   ├── api_admin_products.go   #   CRUD + image upload
│   ├── api_admin_categories.go
│   ├── api_admin_orders.go     #   list, detail, status transition
│   └── models.go               #   Models() for the migration set
├── server/routes/
│   └── shop.go                 # MountShop: paths + guards, nothing else
└── web/pages/                  # package pages, prefixed like aurenor_*.templ
    ├── shop_storefront.templ   #   ShopLanding, ShopProductList, ShopProductCard, ShopProductDetail, ShopCategoryPage
    ├── shop_cart.templ         #   ShopCart
    ├── shop_checkout.templ     #   ShopCheckout, ShopOrderConfirmation
    └── shop_admin.templ        #   ShopAdminDashboard, ShopAdminProducts, ShopAdminProductForm,
                                #   ShopAdminCategories, ShopAdminOrders, ShopAdminOrderDetail
```

Dependency direction, matching `master`: `database -> server/shop (Models) -> shop -> database/models`;
`server/shop -> authentication`; `routes -> server/shop`. `internal/shop` never imports
`net/http` or `internal/server/*`.

---

## 2. Data model

### 2.1 Namespace split with the booking domain

Both domains share one PostgreSQL database and one `models` package. The prefix is the namespace:

| Domain | Type prefix | Table prefix | Files |
|---|---|---|---|
| Booking kernel / appointments / ticketing | `Booking*` | `booking_*` | `models/booking_*.go` |
| Shop | `Shop*` | `shop_*` | `models/shop_*.go` |
| Shared | `User` | `users` | `models/users.go` |

Shop "stock" (`ShopProduct.StockQty`) is a different concept from `BookingInventory` (a
capacity pool with holds). They share nothing. If the shop later needs reservations or a
payment ledger they are `ShopReservation` / `ShopPayment` inside `shop_*`, never a reuse of
`booking_*`.

### 2.2 Models

Money is **int64 minor units** plus a currency code, never floats. Order lines snapshot
title/SKU/price so an order never changes when the product is edited later. `CHECK` constraints
follow the booking models' habit of stating invariants in the tag.

Catalog rows use `gorm.Model` because soft-delete is the right delete for something order
lines point at. (New for this domain: no booking model uses `gorm.Model` or soft-delete;
only `User` does, and there it is paired with an explicit `ID`.) Order rows use explicit
timestamps like `BookingOrder`: an order is never deleted, so a `DeletedAt` column would only
be a lie about the lifecycle.

> **`foreignKey:` tags.** On the has-many sides (`Images`, `Items`) the `Shop` prefix breaks
> GORM's default inference (it would look for `ShopProductID` / `ShopOrderID`), so
> `foreignKey:ProductID` / `foreignKey:OrderID` keep the columns short. This is also new for
> the repo: no booking model declares a GORM association and nothing on `master` calls
> `Preload`. Standard GORM v1.25 usage, just unprecedented here.

```go
// internal/database/models/shop_category.go
package models

import "gorm.io/gorm"

// ShopCategory is a node in the storefront's category tree. ParentID is nil at
// the root. Position orders siblings.
type ShopCategory struct {
	gorm.Model
	Slug        string `json:"slug"        gorm:"uniqueIndex;not null"`
	Name        string `json:"name"        gorm:"not null"`
	Description string `json:"description"`
	ParentID    *uint  `json:"parent_id"   gorm:"index"`
	Position    int    `json:"position"    gorm:"default:0"`
}
```

```go
// internal/database/models/shop_product.go
package models

import "gorm.io/gorm"

type ShopProductStatus string

const (
	ShopProductDraft     ShopProductStatus = "draft"
	ShopProductPublished ShopProductStatus = "published"
	ShopProductArchived  ShopProductStatus = "archived"
)

// ShopProduct is one sellable item. Only published rows are visible on the
// storefront; the admin sees every status. PriceCents is the price at the
// moment of sale — the order line copies it, so editing it later never
// rewrites history.
type ShopProduct struct {
	gorm.Model
	Slug        string            `json:"slug"        gorm:"uniqueIndex;not null"`
	Title       string            `json:"title"       gorm:"not null"`
	Description string            `json:"description"`
	Status      ShopProductStatus `json:"status"      gorm:"index;not null;default:draft"`
	PriceCents  int64             `json:"price_cents" gorm:"not null;check:price_cents >= 0"`
	Currency    string            `json:"currency"    gorm:"not null;default:USD"`
	SKU         string            `json:"sku"         gorm:"index"`
	StockQty    int               `json:"stock_qty"   gorm:"not null;default:0;check:stock_qty >= 0"`
	Featured    bool              `json:"featured"    gorm:"index;default:false"`

	CategoryID *uint              `json:"category_id" gorm:"index"`
	Category   *ShopCategory      `json:"category,omitempty" gorm:"foreignKey:CategoryID"`
	Images     []ShopProductImage `json:"images,omitempty"   gorm:"foreignKey:ProductID"`
}
```

```go
// internal/database/models/shop_product_image.go
package models

import "gorm.io/gorm"

// ShopProductImage is one uploaded image. URL is the public path the file is
// served from (§8); Position orders the gallery, 0 is the cover.
type ShopProductImage struct {
	gorm.Model
	ProductID uint   `json:"product_id" gorm:"index;not null"`
	URL       string `json:"url"        gorm:"not null"`
	Alt       string `json:"alt"`
	Position  int    `json:"position"   gorm:"default:0"`
}
```

```go
// internal/database/models/shop_cart.go
package models

import "time"

// ShopCart is the database-backed cart store, used when Valkey is absent
// (Valkey is optional and fails open — utils.NewValKeyClient returns nil).
// OwnerRef is the key: "user:<id>" | "guest:<sid>", exactly as OwnerRef mints
// it. Data is the JSON-encoded shop.Cart.
type ShopCart struct {
	OwnerRef  string    `gorm:"primaryKey"`
	Data      string    `gorm:"type:text;not null"`
	UpdatedAt time.Time `gorm:"index"` // the DB store's TTL sweep reads this (§3.3)
}
```

```go
// internal/database/models/shop_order.go
package models

import "time"

type ShopOrderStatus string

const (
	ShopOrderPendingPayment ShopOrderStatus = "pending_payment"
	ShopOrderPaid           ShopOrderStatus = "paid"
	ShopOrderFulfilled      ShopOrderStatus = "fulfilled"
	ShopOrderCancelled      ShopOrderStatus = "cancelled"
)

// ShopOrder is a placed order. Number is a 256-bit token and the only thing a
// guest needs to see the order; OwnerRef is the reference the authentication
// layer produces, so a guest's order can be re-pointed at an account on login.
// Shipping fields are a snapshot at purchase time.
type ShopOrder struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Number   string          `json:"number"   gorm:"uniqueIndex;not null"`
	OwnerRef string          `json:"-"        gorm:"index;not null"` // "user:<id>" | "guest:<sid>"
	UserID   *uint           `json:"user_id"  gorm:"index"`          // nil for guest
	Email    string          `json:"email"    gorm:"index;not null"`
	Status   ShopOrderStatus `json:"status"   gorm:"index;not null;default:pending_payment"`
	Currency string          `json:"currency" gorm:"not null"`

	SubtotalCents int64 `json:"subtotal_cents" gorm:"not null"`
	ShippingCents int64 `json:"shipping_cents" gorm:"not null;default:0"`
	TotalCents    int64 `json:"total_cents"    gorm:"not null"`

	ShipName    string `json:"ship_name"`
	ShipAddress string `json:"ship_address"`
	ShipCity    string `json:"ship_city"`
	ShipPostal  string `json:"ship_postal"`
	ShipCountry string `json:"ship_country"`

	// Payment seam (§3.6). Provider names the PaymentProvider that handled the
	// order ("manual" today); ProviderRef is whatever it handed back. PaidAt is
	// set by the transition to paid.
	Provider    string     `json:"provider"     gorm:"not null;default:manual"`
	ProviderRef string     `json:"provider_ref" gorm:"index"`
	PaidAt      *time.Time `json:"paid_at"`

	Items []ShopOrderItem `json:"items,omitempty" gorm:"foreignKey:OrderID"`
}
```

```go
// internal/database/models/shop_order_item.go
package models

import "time"

// ShopOrderItem is an immutable snapshot of one cart line at the moment the
// order was placed. Never join back to ShopProduct for display.
type ShopOrderItem struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	CreatedAt time.Time `json:"created_at"`

	OrderID   uint   `json:"order_id"   gorm:"index;not null"`
	ProductID uint   `json:"product_id" gorm:"index;not null"` // may point at a soft-deleted product
	Title     string `json:"title"      gorm:"not null"`
	SKU       string `json:"sku"`
	UnitCents int64  `json:"unit_cents" gorm:"not null"`
	Quantity  int    `json:"quantity"   gorm:"not null;check:quantity >= 1"`
	LineCents int64  `json:"line_cents" gorm:"not null"` // UnitCents * Quantity
}
```

### 2.3 Migration wiring

Migration is an explicit step on `master` (`clair migrate`, PR #18); startup runs it only with
`DATABASE_AUTO_MIGRATE=true`. The shop registers its tables the way every domain does:

```go
// internal/server/shop/models.go
package shop

import "github.com/erancihan/clair/internal/database/models"

// Models returns the GORM models the shop domain owns, for the migration set
// composed in internal/database.
func Models() []any {
	return []any{
		&models.ShopCategory{},
		&models.ShopProduct{},
		&models.ShopProductImage{},
		&models.ShopCart{},
		&models.ShopOrder{},
		&models.ShopOrderItem{},
	}
}
```

```go
// internal/database/database.go — MigrationModels()
all = append(all, games.Models()...)
all = append(all, booking.Models()...)
all = append(all, shop.Models()...)   // + import "github.com/erancihan/clair/internal/server/shop"
```

No dialect guards: the app is PostgreSQL-only. `CHECK` constraints in the tags run as written.

---

## 3. Domain layer (`internal/shop`)

### 3.1 Errors (`errors.go`)

```go
package shop

import "errors"

var (
	ErrEmptyCart     = errors.New("shop: cart is empty")
	ErrOutOfStock    = errors.New("shop: insufficient stock")
	ErrUnavailable   = errors.New("shop: product no longer available") // unpublished/deleted since it was added
	ErrBadTransition = errors.New("shop: illegal order status transition")
	ErrNotFound      = errors.New("shop: not found")
)
```

Handlers map these to HTTP: `ErrEmptyCart` → 400, `ErrOutOfStock`/`ErrUnavailable` → 409,
`ErrBadTransition` → 400, `ErrNotFound` → 404.

### 3.2 Money (`money.go`)

```go
// FormatCents renders minor units, e.g. (1299, "USD") -> "$12.99". Unmapped
// currencies fall back to "CODE 12.99".
func FormatCents(cents int64, currency string) string {
	sym := map[string]string{"USD": "$", "EUR": "€", "GBP": "£", "TRY": "₺"}[currency]
	if sym == "" {
		sym = currency + " "
	}
	neg := ""
	if cents < 0 {
		neg, cents = "-", -cents
	}
	return fmt.Sprintf("%s%s%d.%02d", neg, sym, cents/100, cents%100)
}
```

### 3.3 Slugs (`slug.go`)

`Slugify(s)` lowercases, replaces non `[a-z0-9]` runs with `-`, trims. `UniqueSlug(tx, table,
base)` returns `base`, or `base-2`, `base-3`, … — the first value with no row in `table`. The
admin create paths call it; `Slug` stays `uniqueIndex` as the backstop against a race.

### 3.4 Catalog (`catalog.go`)

Thin GORM wrappers. Public queries only surface `published` rows; admin variants are unfiltered.

```go
type Catalog struct{ db *gorm.DB }

func NewCatalog(db *gorm.DB) *Catalog { return &Catalog{db: db} }

func (c *Catalog) tx(ctx context.Context) *gorm.DB { return c.db.Session(&gorm.Session{Context: ctx}) }

type ListParams struct {
	CategorySlug string
	Search       string
	Page, PerPage int
}

const defaultPerPage = 12

func (c *Catalog) ListPublished(ctx context.Context, p ListParams) ([]models.ShopProduct, int64, error) {
	if p.PerPage <= 0 { p.PerPage = defaultPerPage }
	if p.Page <= 0    { p.Page = 1 }

	// filter applies the same WHERE/JOIN to a fresh chain. Count and Find each
	// get their own chain: a Model(...) chain is not cloned between finishers, so
	// reusing one statement for both works on GORM v1.25 only by accident of
	// Count restoring clauses in a defer.
	filter := func(q *gorm.DB) *gorm.DB {
		q = q.Model(&models.ShopProduct{}).Where("shop_products.status = ?", models.ShopProductPublished)
		if p.Search != "" {
			q = q.Where("shop_products.title ILIKE ?", "%"+p.Search+"%") // Postgres: case-insensitive
		}
		if p.CategorySlug != "" {
			q = q.Joins("JOIN shop_categories ON shop_categories.id = shop_products.category_id").
				Where("shop_categories.slug = ?", p.CategorySlug)
		}
		return q
	}

	var total int64
	if err := filter(c.tx(ctx)).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var products []models.ShopProduct
	err := filter(c.tx(ctx)).
		Preload("Images", func(db *gorm.DB) *gorm.DB { return db.Order("position ASC") }).
		Order("shop_products.created_at DESC").
		Offset((p.Page - 1) * p.PerPage).Limit(p.PerPage).
		Find(&products).Error
	return products, total, err
}

// BySlug is the storefront lookup: published only.
func (c *Catalog) BySlug(ctx context.Context, slug string) (*models.ShopProduct, error)
// ByID is the admin lookup: any status.
func (c *Catalog) ByID(ctx context.Context, id uint) (*models.ShopProduct, error)
func (c *Catalog) Featured(ctx context.Context, limit int) ([]models.ShopProduct, error)
func (c *Catalog) Categories(ctx context.Context) ([]models.ShopCategory, error)
func (c *Catalog) CategoryBySlug(ctx context.Context, slug string) (*models.ShopCategory, error)

// Admin CRUD
func (c *Catalog) AdminList(ctx context.Context) ([]models.ShopProduct, error)
func (c *Catalog) Create(ctx context.Context, p *models.ShopProduct) error      // assigns UniqueSlug
func (c *Catalog) Update(ctx context.Context, p *models.ShopProduct) error
func (c *Catalog) Delete(ctx context.Context, id uint) error                     // soft delete
func (c *Catalog) AddImage(ctx context.Context, productID uint, url, alt string) error
func (c *Catalog) CreateCategory(ctx context.Context, cat *models.ShopCategory) error
```

Every lookup that can miss returns `ErrNotFound` (wrapping `gorm.ErrRecordNotFound`) so handlers
have one thing to `errors.Is`.

### 3.5 Cart (`cart.go` + two stores)

The cart is keyed on `api_auth.OwnerRef(w, r)` — `"user:<id>"` for a signed-in shopper,
`"guest:<sid>"` otherwise. `OptionalAuthMiddleware` runs in front of every application route,
so the reference is consistent on every cart page and a signed-in shopper never splits into a
guest cart. Prices in the cart are a display cache: checkout re-reads every product (§3.7).

```go
type CartItem struct {
	ProductID uint   `json:"product_id"`
	Slug      string `json:"slug"`
	Title     string `json:"title"`
	SKU       string `json:"sku"`
	UnitCents int64  `json:"unit_cents"`
	Quantity  int    `json:"quantity"`
	ImageURL  string `json:"image_url"`
}

type Cart struct {
	Currency string     `json:"currency"`
	Items    []CartItem `json:"items"`
}

func (c *Cart) SubtotalCents() int64
func (c *Cart) Count() int
// Upsert adds qty (may be negative). A resulting quantity <= 0 removes the line.
func (c *Cart) Upsert(item CartItem, qty int)
// Set replaces the quantity outright (0 removes). Used by /cart/update.
func (c *Cart) Set(productID uint, qty int)
func (c *Cart) Remove(productID uint)

// CartStore persists carts by owner reference.
//
// Load returns a non-nil, possibly empty Cart whenever err == nil: a missing
// key is not an error, so callers can Upsert without a nil check.
type CartStore interface {
	Load(ctx context.Context, ownerRef string) (*Cart, error)
	Save(ctx context.Context, ownerRef string, cart *Cart) error
	Delete(ctx context.Context, ownerRef string) error
}

// NewCartStore picks Valkey when it is up and the shop_carts table otherwise.
func NewCartStore(v valkey.Client, db *gorm.DB) CartStore {
	if v != nil {
		return NewValkeyCartStore(v)
	}
	return NewDBCartStore(db)
}
```

`cart_valkey.go`: key `shop:cart:<ownerRef>`, JSON value, `EX` 7 days, `valkey.IsValkeyNil`
→ empty cart. `cart_db.go`: upsert into `shop_carts` keyed on `OwnerRef`; a `SweepCarts(db,
olderThan)` helper deletes rows with `updated_at < now - 30d`, to be called from the scheduler
later (not part of the build phases; note it in `cart_db.go` and move on).

### 3.6 Payment seam (`payment.go`)

```go
// PaymentProvider is what a real gateway replaces. Begin is called after
// PlaceOrder inside the same request; it returns where to send the shopper.
type PaymentProvider interface {
	Name() string
	// Begin starts payment for the order and returns a URL to redirect to.
	// A provider may return "" to mean "nothing to do, show the confirmation".
	Begin(ctx context.Context, order *models.ShopOrder) (redirectURL string, providerRef string, err error)
}

// ManualProvider is the launch provider: it does nothing, the order stays
// pending_payment, and an admin marks it paid from the CMS.
type ManualProvider struct{}

func (ManualProvider) Name() string { return "manual" }
func (ManualProvider) Begin(context.Context, *models.ShopOrder) (string, string, error) { return "", "", nil }
```

Stripe later: a `StripeProvider` creating a Checkout Session, a `ShopPayment` model, and a
`POST /webhooks/shop/{provider}` route mounted outside every session/CSRF group, verifying with
`api_auth.VerifyProviderSignature`. All inside `shop_*`; nothing shared with booking's webhook.

### 3.7 Checkout (`checkout.go`)

`PlaceOrder` converts a cart into an order in one transaction. It **re-reads every product** —
the cart's prices and titles are only a display cache — and refuses lines whose product is no
longer published (`ErrUnavailable`). Stock is decremented with a conditional `UPDATE`;
`RowsAffected == 0` is the oversell signal and rolls the whole order back.

```go
type ShippingDetails struct {
	Email, Name, Address, City, Postal, Country string
}

type PlaceOrderInput struct {
	Cart     *Cart
	Shipping ShippingDetails
	OwnerRef string // api_auth.OwnerRef(w, r), resolved by the handler
	UserID   *uint  // from api_auth.CurrentUser, nil for guest
	Number   string // api_auth.SecureToken(), minted by the handler
	Provider string // provider.Name()
}

func PlaceOrder(ctx context.Context, db *gorm.DB, in PlaceOrderInput) (*models.ShopOrder, error) {
	if in.Cart == nil || len(in.Cart.Items) == 0 {
		return nil, ErrEmptyCart
	}
	order := &models.ShopOrder{
		Number: in.Number, OwnerRef: in.OwnerRef, UserID: in.UserID,
		Email: in.Shipping.Email, Status: models.ShopOrderPendingPayment,
		Provider: in.Provider,
		ShipName: in.Shipping.Name, ShipAddress: in.Shipping.Address, ShipCity: in.Shipping.City,
		ShipPostal: in.Shipping.Postal, ShipCountry: in.Shipping.Country,
	}

	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, it := range in.Cart.Items {
			// Re-price from the database; the cart is not trusted for money.
			var p models.ShopProduct
			res := tx.Where("id = ? AND status = ?", it.ProductID, models.ShopProductPublished).Limit(1).Find(&p)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return ErrUnavailable
			}
			if order.Currency == "" {
				order.Currency = p.Currency
			}
			dec := tx.Model(&models.ShopProduct{}).
				Where("id = ? AND stock_qty >= ?", p.ID, it.Quantity).
				UpdateColumn("stock_qty", gorm.Expr("stock_qty - ?", it.Quantity))
			if dec.Error != nil {
				return dec.Error
			}
			if dec.RowsAffected == 0 {
				return ErrOutOfStock
			}
			line := p.PriceCents * int64(it.Quantity)
			order.SubtotalCents += line
			order.Items = append(order.Items, models.ShopOrderItem{
				ProductID: p.ID, Title: p.Title, SKU: p.SKU,
				UnitCents: p.PriceCents, Quantity: it.Quantity, LineCents: line,
			})
		}
		order.TotalCents = order.SubtotalCents + order.ShippingCents
		return tx.Create(order).Error
	})
	if err != nil {
		return nil, err
	}
	return order, nil
}
```

`PlaceOrder` takes no `CartStore`. The handler owns the cart: on success it calls
`Carts.Delete(ctx, ownerRef)` **before** redirecting to `/shop/orders/{number}`. A double
submit therefore finds an empty cart and gets `ErrEmptyCart` (§10.4, row 11).

### 3.8 Order status (`order_status.go`)

```go
var allowedTransitions = map[models.ShopOrderStatus][]models.ShopOrderStatus{
	models.ShopOrderPendingPayment: {models.ShopOrderPaid, models.ShopOrderCancelled},
	models.ShopOrderPaid:           {models.ShopOrderFulfilled, models.ShopOrderCancelled},
	models.ShopOrderFulfilled:      {},
	models.ShopOrderCancelled:      {},
}

// Transition moves an order to `to` if the state machine allows it. Same-state
// is a no-op. Moving to paid stamps PaidAt. Cancelling a pending_payment or paid
// order restocks its lines (a fulfilled order is shipped; there is nothing to
// put back, and the map forbids it anyway).
func Transition(ctx context.Context, db *gorm.DB, number string, to models.ShopOrderStatus) (*models.ShopOrder, error)
```

`Transition` runs in one transaction with `SELECT … FOR UPDATE` on the order row (Postgres-only,
same as the kernel) so two admins clicking at once cannot both restock.

### 3.9 Guest hand-over (`guest.go`)

```go
// MigrateGuest merges the guest cart into the account's cart and re-points the
// guest's still-pending orders at the account. Safe to repeat: a second run
// finds no guest cart and matches no rows.
func MigrateGuest(ctx context.Context, db *gorm.DB, carts CartStore, guestRef, userRef string) error {
	guest, err := carts.Load(ctx, guestRef)
	if err != nil {
		return err
	}
	if len(guest.Items) > 0 {
		user, err := carts.Load(ctx, userRef)
		if err != nil {
			return err
		}
		for _, it := range guest.Items {
			user.Upsert(it, it.Quantity) // union; quantities summed
		}
		if user.Currency == "" {
			user.Currency = guest.Currency
		}
		if err := carts.Save(ctx, userRef, user); err != nil {
			return err
		}
		if err := carts.Delete(ctx, guestRef); err != nil {
			return err
		}
	}

	// userIDFromRef parses "user:<id>". The booking domain has the same parser but
	// unexported in internal/server/booking, which internal/shop must not import;
	// guest.go carries its own copy (strings.CutPrefix + ParseUint, zero rejected).
	userID, err := userIDFromRef(userRef)
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Model(&models.ShopOrder{}).
		Where("owner_ref = ? AND status = ?", guestRef, models.ShopOrderPendingPayment).
		Updates(map[string]any{"owner_ref": userRef, "user_id": userID}).Error
}
```

The migrator signature the auth layer calls is `func(ctx, db, guestRef, userRef) error` — it
has no Valkey handle. The registry is an append-only process global with no de-duplication,
and every `Routes()` call (each `newRoutesServer(t)` in tests) constructs a new `API`, so the
registration must be **once per process** and the closure must not capture a per-construction
store. `shop.NewAPI` therefore registers under a `sync.Once`, captures only the long-lived
Valkey client, and builds the store from the `db` the auth layer hands in:

```go
var registerMigratorOnce sync.Once

// in NewAPI
registerMigratorOnce.Do(func() {
	vk := ctx.ValKey // nil when Valkey is off; long-lived otherwise
	api_auth.RegisterGuestMigrator("shop", func(c context.Context, db *gorm.DB, guestRef, userRef string) error {
		return shop.MigrateGuest(c, db, shop.NewCartStore(vk, db), guestRef, userRef)
	})
})
```

In tests Valkey is nil, so the migrator uses the login request's own `db` and never a stale
one.

Quantities are summed on merge and **not** capped at stock here: stock is enforced once, at
checkout (§3.7), which is also where the shopper gets a message they can act on.

### 3.10 Seed (`seed.go`)

`Seed(db *gorm.DB) (Report, error)`: idempotent `FirstOrCreate` by slug — three categories,
around ten published products with stock, one draft, one archived — so the storefront has
something to show. Wired as a cobra subcommand in Phase 2. `master` has no seed command; the
parallel tree adds `clair seed` (`internal/cmd/seed.go`, Aurenor catalog, same
`FirstOrCreate`-by-slug shape). If that has landed, add one `shop.Seed(db)` call to it;
otherwise register a `clair seed-shop` command and fold it in later.

---

## 4. HTTP layer (`internal/server/shop`) + route table

### 4.1 `API` struct (`api.go`)

Mirrors the booking domain's `API`: the route table lives in `routes/`, this package only
says what each handler does.

```go
package shop

type API struct {
	ctx      server_context.BackEndContext
	catalog  *shop.Catalog
	carts    shop.CartStore
	provider shop.PaymentProvider
}

var registerMigratorOnce sync.Once

func NewAPI(ctx server_context.BackEndContext) *API {
	// A visitor who fills a cart as a guest and then signs in keeps it. Registered
	// once per process and stateless apart from the Valkey client — see §3.9 for
	// why a closure over this API's own store would be wrong.
	registerMigratorOnce.Do(func() {
		vk := ctx.ValKey
		api_auth.RegisterGuestMigrator("shop", func(c context.Context, db *gorm.DB, guestRef, userRef string) error {
			return shop.MigrateGuest(c, db, shop.NewCartStore(vk, db), guestRef, userRef)
		})
	})

	return &API{
		ctx:      ctx,
		catalog:  shop.NewCatalog(ctx.DBConn),
		carts:    shop.NewCartStore(ctx.ValKey, ctx.DBConn),
		provider: shop.ManualProvider{},
	}
}

// NotFound is the subtree fallback the route table mounts under /shop/ and
// /shop/admin/ (§4.3): a real 404 for any path or verb nothing else claimed.
func (a *API) NotFound() http.HandlerFunc

// ---- storefront ----
func (a *API) Landing() http.HandlerFunc
func (a *API) ProductList() http.HandlerFunc
func (a *API) ProductDetail() http.HandlerFunc
func (a *API) Category() http.HandlerFunc

// ---- cart ----
func (a *API) ViewCart() http.HandlerFunc
func (a *API) AddToCart() http.HandlerFunc
func (a *API) UpdateCart() http.HandlerFunc
func (a *API) RemoveFromCart() http.HandlerFunc

// ---- checkout ----
func (a *API) Checkout() http.HandlerFunc
func (a *API) SubmitCheckout() http.HandlerFunc
func (a *API) OrderConfirmation() http.HandlerFunc

// ---- admin ----
func (a *API) AdminDashboard() http.HandlerFunc
func (a *API) AdminProducts() http.HandlerFunc
func (a *API) AdminProductForm() http.HandlerFunc   // new + edit
func (a *API) AdminProductCreate() http.HandlerFunc
func (a *API) AdminProductUpdate() http.HandlerFunc
func (a *API) AdminProductDelete() http.HandlerFunc
func (a *API) AdminImageUpload() http.HandlerFunc
func (a *API) AdminCategories() http.HandlerFunc
func (a *API) AdminCategoryCreate() http.HandlerFunc
func (a *API) AdminOrders() http.HandlerFunc
func (a *API) AdminOrderDetail() http.HandlerFunc
func (a *API) AdminOrderStatus() http.HandlerFunc
```

### 4.2 Helpers (`api_http.go`)

```go
// db binds GORM to the request context (same helper as booking's api_http.go).
func db(ctx server_context.BackEndContext, r *http.Request) *gorm.DB

// page renders body inside the site shell. PageShell mints/persists the CSRF
// token and resolves the user menu, and it writes to w, so it runs before the
// body — passing it as the argument gets that ordering for free.
func page(w http.ResponseWriter, r *http.Request, title string, body templ.Component) {
	templ.Handler(web.Base(api_auth.PageShell(w, r, title), body)).ServeHTTP(w, r)
}

// notFound answers a real 404 with the site's not-found page. The status goes
// through templ.WithStatus: templ.Handler sets Content-Type after it writes the
// header, so a bare w.WriteHeader(404) before ServeHTTP would drop the header.
func notFound(w http.ResponseWriter, r *http.Request) {
	render(w, r, http.StatusNotFound, "Not found", pages.NotFound())
}

// render is page() with an explicit status, for re-rendering a form with
// validation errors as 400 (and for notFound above).
func render(w http.ResponseWriter, r *http.Request, status int, title string, body templ.Component) {
	shell := api_auth.PageShell(w, r, title) // before any body write: may set a cookie
	templ.Handler(web.Base(shell, body), templ.WithStatus(status)).ServeHTTP(w, r)
}

func parseID(r *http.Request, name string) (uint, bool)     // as booking: 0 and garbage rejected
func formInt(r *http.Request, name string, def int) int      // Atoi with default
func formCents(r *http.Request, name string) (int64, error)  // "12.99" -> 1299, rejects negatives/garbage
```

Forms carry the CSRF token as a hidden field. The token is on the shell the handler already
built, so views that contain a form take it as an argument:

```go
shell := api_auth.PageShell(w, r, "Checkout")
templ.Handler(web.Base(shell, pages.ShopCheckout(shell.CSRFToken, cart, nil, nil))).ServeHTTP(w, r)
```

(`ShopCheckout(csrfToken string, cart *shop.Cart, errs, values map[string]string)` — the two
maps are nil on first render and filled when re-rendering after validation, §4.6.)

```templ
<input type="hidden" name="csrf_token" value={ csrfToken }/>
```

(`api_auth.CSRFFieldName` is `"csrf_token"`; the middleware reads the header first, then the
form field.)

### 4.3 Route table (`internal/server/routes/shop.go`)

Guards are the decision this file exists to make explicit, in the same four classes the booking
table uses: public reads, guest-allowed writes (behind `CSRF()`), admin (behind
`AdminMiddleware` + `CSRF()`). The shop has no machine route yet.

> **Three `net/http` ServeMux facts that shape this table.**
>
> 1. **The site has a method-less catch-all `/`** (`server.go`, `mountPages`) that renders
>    the not-found page with **200**, pinned by `test/mount_seam_test.go`. Anything the shop
>    does not register — an unknown `/shop/*` path, a wrong verb on a registered path —
>    falls through to it. ServeMux never answers 405 while that pattern exists, because `/`
>    matches every method. The shop therefore mounts its **own method-less subtree
>    fallbacks**, `/shop/` and `/shop/admin/`, that answer a real 404 (`api.NotFound()`).
>    Wrong verbs land there too, so "wrong verb" is a **404, not 405**, everywhere under
>    `/shop/`. The one exception is the bare `/shop`: a `POST /shop` matches neither
>    `GET /shop` nor `/shop/` and reaches the site catch-all (200). Not worth a special case.
> 2. **A pattern ending in `/` matches a whole subtree.** That is exactly what the fallbacks
>    above rely on, and exactly why the landing must **not** be registered as `"GET /"`
>    inside the group (it would be `/shop/` and shadow the fallback with a 200). The landing
>    is registered exactly: `GET /shop` from the top-level router and `GET /shop/{$}` (`{$}`
>    = end of path, Go 1.22+) as a redirect. Same for `/shop/admin`. That is also why the
>    canonical URL has no trailing slash: it is the form that can be registered exactly.
>    (The booking route table documents this trap for `/appointments/schedules/`.)
> 3. **Exactly one space between method and path.** `router.HandleFunc` splits on the first
>    space; `"GET  /cart"` (two spaces, e.g. for alignment) registers the pattern
>    `/shop/ /cart` and the real URL silently hits the fallback. Don't pad.
>
> All 28 patterns below plus `/`, `GET /static/`, `GET /public/` were registered on a real
> Go 1.25 mux during review: no conflict panics; `/shop`, `/shop/{$}`, `/shop/`,
> `/shop/products…`, `/shop/admin…` are all disjoint by precedence.

```go
package routes

// MountShop registers every route the shop domain owns.
func MountShop(r *router.Router, ctx server_context.BackEndContext) {
	api := shop.NewAPI(ctx)

	// Canonical landing is the bare /shop, registered from the top level so it
	// lands on an exact pattern (see the ServeMux note above). /shop/ redirects.
	r.HandleFunc("GET /shop", api.Landing())
	r.HandleFunc("GET /shop/{$}", redirectTo("/shop"))

	r.Group("shop", func(store *router.Router) {
		// Subtree fallback: a real 404 for any path or verb under /shop/ nothing
		// below claims. Without it those requests reach the site's "/" catch-all
		// and come back 200. Method-less on purpose.
		store.HandleFunc("/", api.NotFound())

		// ---- public reads ----
		store.HandleFunc("GET /products", api.ProductList())
		store.HandleFunc("GET /products/{slug}", api.ProductDetail())
		store.HandleFunc("GET /categories/{slug}", api.Category())
		store.HandleFunc("GET /cart", api.ViewCart())
		store.HandleFunc("GET /checkout", api.Checkout())
		store.HandleFunc("GET /orders/{number}", api.OrderConfirmation())

		// ---- guest-allowed writes ----
		// Ownership follows api_auth.OwnerRef: the account when there is one,
		// the per-browser guest reference otherwise. Guest checkout is a locked
		// decision, so none of this sits behind AuthMiddleware.
		store.Group("", func(guest *router.Router) {
			guest.HandleFunc("POST /cart/add", api.AddToCart())
			guest.HandleFunc("POST /cart/update", api.UpdateCart())
			guest.HandleFunc("POST /cart/remove", api.RemoveFromCart())
			guest.HandleFunc("POST /checkout", api.SubmitCheckout())
		}, api_auth.CSRF())

		// ---- admin: the CMS ----
		// CSRF sits alongside AdminMiddleware, not instead of it: the session
		// cookie is sent cross-site too. The dashboard is registered exactly
		// (/shop/admin) for the subtree reason above; /shop/admin/ redirects.
		// Both exact patterns carry the same guards as the group.
		guarded := store.Middleware(api_auth.AdminMiddleware(ctx), api_auth.CSRF())
		guarded.HandleFunc("GET /admin", api.AdminDashboard())
		guarded.HandleFunc("GET /admin/{$}", redirectTo("/shop/admin"))
		store.Group("admin", func(admin *router.Router) {
			// Guarded subtree fallback: an unknown /shop/admin/* path still runs
			// AdminMiddleware (302/401/403) and only then 404s — the existence of
			// admin routes is not something an anonymous probe gets to learn.
			admin.HandleFunc("/", api.NotFound())

			admin.HandleFunc("GET /products", api.AdminProducts())
			admin.HandleFunc("GET /products/new", api.AdminProductForm())
			admin.HandleFunc("POST /products", api.AdminProductCreate())
			admin.HandleFunc("GET /products/{id}/edit", api.AdminProductForm())
			admin.HandleFunc("POST /products/{id}", api.AdminProductUpdate())
			admin.HandleFunc("POST /products/{id}/delete", api.AdminProductDelete())

			admin.HandleFunc("GET /categories", api.AdminCategories())
			admin.HandleFunc("POST /categories", api.AdminCategoryCreate())

			admin.HandleFunc("GET /orders", api.AdminOrders())
			admin.HandleFunc("GET /orders/{number}", api.AdminOrderDetail())
			admin.HandleFunc("POST /orders/{number}/status", api.AdminOrderStatus())
		}, api_auth.AdminMiddleware(ctx), api_auth.CSRF())

		// Image upload: the body cap has to sit OUTSIDE CSRF(). With no
		// X-CSRF-Token header, CSRF() reads the form field via PostFormValue,
		// which parses the whole multipart body (32 MB in-memory threshold, rest
		// spilled to temp files) before any handler runs. A cap applied inside
		// the handler would be dead code. Group middleware appends after the
		// parent's, so the cap is registered from store.Middleware with the
		// guards listed after it: cap outermost, then admin, then CSRF.
		store.Middleware(maxBody(maxUploadBytes), api_auth.AdminMiddleware(ctx), api_auth.CSRF()).
			HandleFunc("POST /admin/products/{id}/images", api.AdminImageUpload())
	})
}
```

`maxBody(n)` is a small middleware in `routes/`: answer **413** at once when
`r.ContentLength > n` (browsers always send it for multipart), and wrap `r.Body` in
`http.MaxBytesReader(w, r.Body, n)` for the chunked case. In the chunked case an oversize
body makes `PostFormValue` fail inside `CSRF()`, which then answers 403 rather than 413; that
is acceptable and noted in the §10.5 upload table.

The admin access-control test (§10.5) covers the two exact patterns as well as the group, so
a future edit that registers one on bare `store` fails a test rather than shipping an open
dashboard. `redirectTo` is a three-line helper in `routes/` (`http.Redirect(..., 301)`).

Wire it with one line in `server.Routes()` under the `// ---- domains ----` block:

```go
routes.MountShop(app, s.context)
```

and one entry in `components.NavLinks`: `{Href: "/shop", Label: "Shop"}`.

### 4.4 A storefront handler (`api_storefront.go`)

```go
func (a *API) ProductDetail() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		product, err := a.catalog.BySlug(r.Context(), r.PathValue("slug"))
		if errors.Is(err, shop.ErrNotFound) {
			notFound(w, r)
			return
		}
		if err != nil {
			a.ctx.Logger.Error("product detail", zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		shell := api_auth.PageShell(w, r, product.Title)
		templ.Handler(web.Base(shell, pages.ShopProductDetail(shell.CSRFToken, product))).ServeHTTP(w, r)
	}
}
```

### 4.5 Add-to-cart (`api_cart.go`)

```go
func (a *API) AddToCart() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner := api_auth.OwnerRef(w, r) // user:<id> or guest:<sid>; mints the sid cookie if needed
		qty := formInt(r, "qty", 1)
		if qty < 1 {
			qty = 1
		}

		p, err := a.catalog.BySlug(r.Context(), r.FormValue("slug"))
		if errors.Is(err, shop.ErrNotFound) {
			http.Error(w, "product not found", http.StatusNotFound)
			return
		}
		if err != nil { /* 500 as above */ }

		cart, err := a.carts.Load(r.Context(), owner)
		if err != nil {
			http.Error(w, "cart unavailable", http.StatusInternalServerError)
			return
		}
		img := ""
		if len(p.Images) > 0 {
			img = p.Images[0].URL
		}
		cart.Upsert(shop.CartItem{
			ProductID: p.ID, Slug: p.Slug, Title: p.Title, SKU: p.SKU,
			UnitCents: p.PriceCents, ImageURL: img,
		}, qty)
		cart.Currency = p.Currency
		if err := a.carts.Save(r.Context(), owner, cart); err != nil {
			http.Error(w, "cart unavailable", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/shop/cart", http.StatusSeeOther)
	}
}
```

Price comes from the database, never the form. `OwnerRef` must be called **before** any body
write since it may set the `sid` cookie; it is the first line for that reason.

### 4.6 Checkout submit (`api_checkout.go`)

1. `owner := api_auth.OwnerRef(w, r)`; `identity, _ := api_auth.CurrentUser(r.Context())`.
2. Load cart; empty → `render(w, r, 400, "Cart", pages.ShopCart(token, cart))` with the
   empty-cart notice (§10.4 `POST /shop/checkout` row 6 pins 400; the 303 belongs only to
   `GET /shop/checkout`).
3. Validate shipping fields (all required; email must parse with `net/mail`). Failure →
   `render(w, r, 400, "Checkout", pages.ShopCheckout(token, cart, errs, values))`.
4. `order, err := shop.PlaceOrder(ctx, db(...), PlaceOrderInput{…, Number: api_auth.SecureToken(), Provider: a.provider.Name()})`.
   Map `ErrOutOfStock`/`ErrUnavailable` → re-render checkout as 409 naming the line.
5. `a.carts.Delete(ctx, owner)` — cart is gone before the redirect.
6. `url, ref, err := a.provider.Begin(ctx, order)`; store `ref` if non-empty; redirect to
   `url` if non-empty, else to `/shop/orders/{number}` (303).

Order confirmation (`GET /shop/orders/{number}`): look up by `Number`; if a `CurrentUser` is
present and neither `order.OwnerRef == user:<id>` nor `order.UserID == id`, answer 404 (never
403 — do not confirm the number exists). A guest sees the order on the number alone; 256 bits
is the guard.

---

## 5. Storefront views (`internal/web/pages/shop_*.templ`)

`templ` components in `package pages`, prefixed `Shop*` the way the booking simulation pages
are prefixed `Aurenor*`. They render inside `web.Base` and inherit header (with user menu),
footer, dark mode and Tailwind. Tailwind Plus "Ecommerce" markup drops into these files; the
classes below are already in that idiom.

Components take typed data and, where they contain a form, the CSRF token:

```templ
package pages

import (
	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/shop"
)

templ ShopProductList(products []models.ShopProduct, total int64, page, perPage int, search, category string) { … }
templ ShopProductCard(p models.ShopProduct) {
	<a href={ templ.SafeURL("/shop/products/" + p.Slug) } class="group block">
		<div class="aspect-square w-full overflow-hidden rounded-2xl bg-zinc-100 dark:bg-zinc-800">
			if len(p.Images) > 0 {
				<img src={ p.Images[0].URL } alt={ p.Images[0].Alt } class="h-full w-full object-cover transition group-hover:scale-105"/>
			}
		</div>
		<h3 class="mt-4 text-sm font-medium text-zinc-800 dark:text-zinc-100">{ p.Title }</h3>
		<p class="mt-1 text-sm text-zinc-600 dark:text-zinc-400">{ shop.FormatCents(p.PriceCents, p.Currency) }</p>
	</a>
}

templ ShopProductDetail(csrfToken string, p *models.ShopProduct) {
	… title, price, description, gallery …
	<form action="/shop/cart/add" method="POST" class="mt-8 flex items-center gap-4">
		<input type="hidden" name="csrf_token" value={ csrfToken }/>
		<input type="hidden" name="slug" value={ p.Slug }/>
		<input type="number" name="qty" value="1" min="1" class="w-20 rounded-md border …"/>
		<button type="submit" class="rounded-md bg-indigo-600 px-6 py-2.5 text-sm font-semibold text-white hover:bg-indigo-500">Add to cart</button>
	</form>
}

templ ShopCart(csrfToken string, cart *shop.Cart) { … per-line update/remove forms, subtotal, checkout link … }
templ ShopCheckout(csrfToken string, cart *shop.Cart, errs map[string]string, values map[string]string) { … }
templ ShopOrderConfirmation(o *models.ShopOrder) { … }
templ ShopLanding(featured []models.ShopProduct, categories []models.ShopCategory) { … }
templ ShopCategoryPage(cat *models.ShopCategory, products []models.ShopProduct, total int64, page, perPage int) { … }
```

Pagination is a `?page=N` query string; the list components render prev/next links from
`total`, `page`, `perPage`.

Regenerate after adding files: `go generate ./...` (the `//go:generate` in `internal/web/embed.go`)
or `make assets`.

---

## 6. Admin CMS (`/shop/admin`)

Server-rendered CRUD, no SPA. Product forms are `multipart/form-data` so image upload shares
the form. Validation failures re-render the form with `400` and the submitted values.

```go
func (a *API) AdminProductCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		p, errs := productFromForm(r) // title required; price via formCents; status in {draft,published,archived}
		if len(errs) > 0 {
			shell := api_auth.PageShell(w, r, "New product")
			w.WriteHeader(http.StatusBadRequest)
			templ.Handler(web.Base(shell, pages.ShopAdminProductForm(shell.CSRFToken, p, "/shop/admin/products", errs))).ServeHTTP(w, r)
			return
		}
		if err := a.catalog.Create(r.Context(), p); err != nil { /* 500 */ }
		http.Redirect(w, r, "/shop/admin/products", http.StatusSeeOther)
	}
}
```

`formCents` parses the decimal string with `strconv.ParseFloat` then `math.Round(f*100)`, and
rejects negatives and NaN. (A float is acceptable here because the input is a two-decimal
form field, and rounding makes it exact; the database never sees a float.)

Order management: list with optional `?status=` filter, detail with a status `<select>` posting
to `/shop/admin/orders/{number}/status`; the handler calls `shop.Transition` and maps
`ErrBadTransition` → 400 with the current status re-rendered.

---

## 7. Auth & identity — consume the shared layer

Everything below is imported from `internal/server/authentication` (`api_auth`). The shop
builds no login, session, role, token or CSRF code.

| Symbol | Shop use |
|---|---|
| `OptionalAuthMiddleware` | already on every app route via `server.Routes()`; nothing to mount |
| `CurrentUser(ctx)` → `Identity{UserID, Role}` | who is checking out; order scoping on the confirmation page |
| `OwnerRef(w, r)` → `"user:<id>"` / `"guest:<sid>"` | cart key and `ShopOrder.OwnerRef` |
| `RegisterGuestMigrator("shop", fn)` | merge guest cart + re-point pending orders on login (§3.9) |
| `AdminMiddleware(ctx)` | every `/shop/admin*` route: 302 `/login?next=…` for browsers, 401 for JSON callers, 403 for role `user` |
| `CSRF()` | every shop `POST`; token via hidden `csrf_token` field or `X-CSRF-Token` header |
| `PageShell(w, r, title)` | every rendered page: mints/persists the CSRF token, fills the user menu |
| `SecureToken()` | `ShopOrder.Number` |

Behaviour inherited and not re-implemented: auth failure is content-negotiated; `/login?next=`
accepts same-origin absolute paths only; `SESSION_KEY` is mandatory in production; the `sid`
cookie is `HttpOnly`, `Path=/`, `SameSite=Lax`, one year.

Sign-in-to-continue links point at `/login?next=/shop/checkout`.

---

## 8. Image uploads

Files go under `web.Public()` (env `PUBLIC_PATH`, default `internal/web/public`; the compose
file mounts a volume there) and are served by the existing `GET /public/` route.

Rules, each pinned by a §10.6 row:

- Size cap `maxUploadBytes = 10 << 20`, enforced by the `maxBody` middleware mounted
  **outside** `CSRF()` in the route table (§4.3): `Content-Length` over the cap → 413 before
  anything is read; `MaxBytesReader` backs that up for chunked bodies. The handler itself does
  not cap — by the time it runs, `CSRF()` has already parsed the multipart body to find the
  `csrf_token` field, so a cap there would be dead code. The handler just calls
  `r.FormFile("image")`.
- Sniff the first 512 bytes with `http.DetectContentType`; allow `image/png`, `image/jpeg`,
  `image/webp`, `image/gif` only. The client's `Content-Type` header is not consulted.
- The stored name is **never** the client's: `products/<productID>-<SecureToken()[:16]>.<ext>`
  with `ext` derived from the sniffed type. Traversal is impossible by construction.
- `os.MkdirAll(filepath.Join(web.Public(), "products"), 0o755)`; write; then
  `catalog.AddImage(ctx, id, "/public/"+name, alt)`.
- Add `internal/web/public/products/` to `.gitignore`.

Upgrade path: an `ImageStore` interface (`Put(ctx, name, r) (url, error)`) with the local
implementation above; an S3 implementation is a later swap (`aws-sdk-go` is already a dependency).

---

## 9. Assets / Tailwind

- `internal/web/css/main.css` is `@import "tailwindcss";` (v4). v4 auto-detects sources, which
  is why pages work today even though `tailwind.config.js` only lists `login.templ`. Make it
  explicit for the new files: add `@source "../";` under the import.
- No new JS build step. `make assets` (`npm run css` + `go generate ./...`) and `make dev`
  (`air`, watches `.templ`) are unchanged.

---

## 10. Testing

Endpoint tests drive the **real** `server.Routes()` through `httptest` against Postgres, the
way `test/mount_seam_test.go` and the aurenor tests do. Domain logic gets unit tests in
`internal/shop`. Postgres comes from `DATABASE_URL` (`make db-up` starts the compose service);
`make test` runs `go test ./test/... -v`.

> **Use the harness that exists.** `master` has no testcontainers dependency and does not need
> one. The helpers below are already in `test/` (auth work) and `internal/testsupport`:
>
> | Helper | Use |
> |---|---|
> | `newRoutesServer(t)` | `httptest.Server` over `server.NewBackEnd(ctx, zap.NewNop(), nil, db).Routes()` — nil Valkey, so the cart uses `shop_carts` |
> | `newAuthTestDB(t)` | drops app tables and re-runs `database.Migrate` (includes `shop.Models()` once wired) |
> | `createUser(t, db, email, pw, role)` | seed an admin (`"admin"`) or shopper (`"user"`) |
> | `jarClient(t)` | cookie-jar client (session + `sid`); set `CheckRedirect` to `http.ErrUseLastResponse` to assert 303s |
> | `readBody(t, resp)`, `csrfMetaToken(t, body)` | read a page; pull the token from `<meta name="csrf-token">` |
> | `testsupport.PostgresDB(t, models...)` | per-test isolated schema for domain unit tests |
>
> The tests need one new helper: `loginRoutes(t, client, srvURL, email, pw)` posting JSON to
> `/api/v1/auth/login` on the routes server (the existing `loginJSON` targets the auth-only
> server; the endpoint requires `Content-Type: application/json`). And a
> `shopForm(t, client, url, token, values)` that posts `application/x-www-form-urlencoded`
> with `csrf_token` set.
>
> **`PUBLIC_PATH` in tests.** `web.Public()` defaults to the *relative* `internal/web/public`,
> and `go test ./test/...` runs with `test/` as the working directory, so uploads would land
> in `test/internal/web/public/`. Upload tests `t.Setenv("PUBLIC_PATH", t.TempDir())`. For the
> same reason the plan asserts nothing about `/static/` or `/public/` (the CSS is
> git-ignored build output; the favicon path is CWD-dependent).

CSRF in tests: `GET` any shop page first, take the token from the meta tag, send it as the
`csrf_token` field. A POST without it must get `403` — that is the CSRF row in every table below.

### 10.1 Unit tests (`internal/shop`, no HTTP)

| # | Target | Scenario | Expected |
|---|---|---|---|
| 1 | `Cart.Upsert` / `Set` / `Remove` | add new; merge qty; qty ≤ 0 removes; Set 0 removes | items & quantities correct |
| 2 | `Cart.SubtotalCents` / `Count` | mixed lines | Σ(unit×qty); Σ qty |
| 3 | `FormatCents` | 1299 USD; 0; −1299; `JPY` | `$12.99`, `$0.00`, `-$12.99`, `JPY 12.99` |
| 4 | `Slugify` / `UniqueSlug` | spaces/case/punctuation; existing `blue-shirt` | `blue-shirt`; `blue-shirt-2`, `-3` (Postgres) |
| 5 | `PlaceOrder` | snapshot lines; subtotal; re-price from DB (cart price ignored); unpublished line → `ErrUnavailable`; qty > stock → `ErrOutOfStock` with **no** order and stock unchanged; empty cart → `ErrEmptyCart` (Postgres) |
| 6 | `Transition` | pending→paid stamps `PaidAt`; paid→fulfilled; pending→cancelled restocks; fulfilled→anything → `ErrBadTransition`; same-state no-op (Postgres) |
| 7 | `MigrateGuest` | guest cart merges into user cart, quantities summed, guest cart deleted; pending guest orders re-pointed with `user_id`; second run is a no-op (Postgres, DB cart store) |
| 8 | DB cart store | Load missing → empty non-nil; Save/Load round-trip; Delete |

### 10.2 Storefront (public reads)

Conventions: *Type* tags coverage (Happy, Validation, AuthN, AuthZ, Not found, Method, Boundary,
Edge, State, Security). Successful POSTs are `303` with `Location`. 🚩 marks an assertion that
pins a decision from §12.

#### `GET /shop`

| # | Type | Scenario | State | Request | Status | Result |
|---|---|---|---|---|---|---|
| 1 | Happy | Landing renders | published products, categories | `GET /shop` | 200 | shell, featured cards, category links |
| 2 | Edge | Empty catalog | none | `GET /shop` | 200 | empty state |
| 3 | Edge | Only drafts | all draft | `GET /shop` | 200 | no cards |
| 4 | Edge | Trailing slash | — | `GET /shop/` | 301 → `/shop` 🚩 | canonical bare path |
| 5 | Method | Wrong verb on bare `/shop` | — | `POST /shop` | 200 | site catch-all; not asserted (§4.3 note 1) |

#### `GET /shop/products`

| # | Type | Scenario | State | Request | Status | Result |
|---|---|---|---|---|---|---|
| 1 | Happy | First page | >12 published | `GET /shop/products` | 200 | 12 cards, newest first |
| 2 | Happy | Page 2 | >12 published | `?page=2` | 200 | remaining |
| 3 | Happy | Search | "Blue Shirt" | `?search=blue` | 200 | case-insensitive match (`ILIKE`) |
| 4 | Happy | Category filter | "hats" populated | `?category=hats` | 200 | only that category |
| 5 | Edge | Search no match | — | `?search=zzz` | 200 | empty grid |
| 6 | Edge | Drafts excluded | mixed | — | 200 | drafts absent |
| 7 | Boundary | Page beyond range | 5 products | `?page=99` | 200 | empty grid |
| 8 | Boundary | page ≤ 0 / non-numeric | — | `?page=0`, `?page=abc` | 200 | page 1 |
| 9 | Edge | Unknown category filter | — | `?category=nope` | 200 🚩 | empty list, consistent with search |
| 10 | Security | Search XSS | — | `?search=<script>` | 200 | escaped (templ) |
| 11 | Method | Wrong verb | — | `POST` | 404 (fallback) | |

#### `GET /shop/products/{slug}`

| # | Type | Scenario | State | Request | Status | Result |
|---|---|---|---|---|---|---|
| 1 | Happy | Published | slug `x` | `GET …/x` | 200 | title, `$12.99`, add-to-cart form with `csrf_token` |
| 2 | Edge | No images | — | `GET …/x` | 200 | no `<img>`, no crash |
| 3 | Not found | Unknown | — | `GET …/ghost` | **404** 🚩 | not-found page |
| 4 | Not found | Draft | — | `GET …/d` | 404 | |
| 5 | Not found | Archived | — | `GET …/a` | 404 | |
| 6 | Not found | Soft-deleted | — | `GET …/gone` | 404 | |
| 7 | Security | Malicious slug | — | `GET …/<script>` | 404 | escaped, no traversal |
| 8 | Method | Wrong verb | — | `POST …/x` | 404 (fallback) | |

#### `GET /shop/categories/{slug}`

| # | Type | Scenario | State | Request | Status | Result |
|---|---|---|---|---|---|---|
| 1 | Happy | With products | "hats" | `GET …/hats` | 200 | that category's published |
| 2 | Edge | Empty category | exists, none | `GET …/hats` | 200 | empty state |
| 3 | Happy | Page 2 | >12 | `?page=2` | 200 | |
| 4 | Not found | Unknown | — | `GET …/none` | **404** | |
| 5 | Method | Wrong verb | — | `POST …/hats` | 404 (fallback) | |

### 10.3 Cart

#### `GET /shop/cart`

| # | Type | Scenario | State | Request | Status | Result |
|---|---|---|---|---|---|---|
| 1 | Happy | Populated | items in cart | `GET` | 200 | lines, qty, line totals, subtotal, `csrf_token` in each form |
| 2 | Happy | Empty | — | `GET` | 200 | empty message, checkout link absent |
| 3 | Edge | First visit | no `sid` | `GET` | 200 | `Set-Cookie: sid` (HttpOnly, Path=/, Lax) |
| 4 | State | Signed-in shopper | logged in, items added while logged in | `GET` | 200 | same cart (keyed `user:<id>`) |
| 5 | Method | Wrong verb | — | `PUT` | 404 (fallback) | |

#### `POST /shop/cart/add`

| # | Type | Scenario | State | Request | Status | Result |
|---|---|---|---|---|---|---|
| 1 | Happy | Add new | published `x` | `slug=x&qty=1` | 303 → `/shop/cart` | line added |
| 2 | Happy | Merge qty | `x` qty 1 | `slug=x&qty=2` | 303 | qty 3, one line |
| 3 | Validation | Missing slug | — | `qty=1` | 404 | |
| 4 | Not found | Unknown slug | — | `slug=ghost` | 404 | |
| 5 | Edge | Draft | — | `slug=d` | 404 | |
| 6 | Boundary | qty missing / 0 / negative | — | — | 303 | qty 1 |
| 7 | Boundary | Non-numeric qty | — | `qty=abc` | 303 | qty 1 |
| 8 | Boundary | Huge qty | — | `qty=999999` | 303 | accepted; stock enforced at checkout 🚩 |
| 9 | Security | Price tampering | — | `price=1` | 303 | price from DB |
| 10 | Security | CSRF missing | — | no token | **403** | |
| 11 | Method | Wrong verb | — | `GET` | 404 (fallback) | |

#### `POST /shop/cart/update`

| # | Type | Scenario | State | Request | Status | Result |
|---|---|---|---|---|---|---|
| 1 | Happy | Set qty | line exists | `product_id=1&qty=5` | 303 | qty 5 |
| 2 | Edge | qty 0 removes | — | `qty=0` | 303 | line gone |
| 3 | Edge | Not in cart | — | `product_id=99` | 303 | no-op |
| 4 | Validation | Missing / non-numeric id | — | — | 400 🚩 | no crash |
| 5 | Security | CSRF missing | — | — | 403 | |
| 6 | Method | Wrong verb | — | `GET` | 404 (fallback) | |

#### `POST /shop/cart/remove`

| # | Type | Scenario | State | Request | Status | Result |
|---|---|---|---|---|---|---|
| 1 | Happy | Remove | line exists | `product_id=1` | 303 | gone |
| 2 | Edge | Absent / twice | — | — | 303 | no-op |
| 3 | Validation | Missing id | — | — | 400 🚩 | |
| 4 | Security | CSRF missing | — | — | 403 | |
| 5 | Method | Wrong verb | — | `GET` | 404 (fallback) | |

#### Guest → login merge (cross-cutting)

| # | Type | Scenario | State | Steps | Result |
|---|---|---|---|---|---|
| 1 | State | Merge on login | guest cart {x:1}; user cart {x:1, y:2} | add as guest, login via `/api/v1/auth/login`, `GET /shop/cart` | {x:2, y:2}; guest key gone |
| 2 | State | Login with no guest cart | — | login, view | user cart unchanged |
| 3 | State | Pending guest order re-pointed | guest placed order | login | `owner_ref=user:<id>`, `user_id` set |

### 10.4 Checkout & orders

#### `GET /shop/checkout`

| # | Type | Scenario | State | Request | Status | Result |
|---|---|---|---|---|---|---|
| 1 | Happy | Form | non-empty cart | `GET` | 200 | shipping form + summary + `csrf_token` |
| 2 | Edge | Empty cart | — | `GET` | 303 → `/shop/cart` 🚩 | |
| 3 | Method | Wrong verb | — | `PUT` | 404 (fallback) | |

#### `POST /shop/checkout`

| # | Type | Scenario | State | Request | Status | Result |
|---|---|---|---|---|---|---|
| 1 | Happy | Guest order | cart, valid fields | all fields | 303 → `/shop/orders/{number}` | order + items `pending_payment`; `owner_ref=guest:<sid>`; stock decremented; cart empty |
| 2 | Happy | Signed-in order | logged in | — | 303 | `owner_ref=user:<id>`, `user_id` set |
| 3 | Edge | Snapshot integrity | order placed, product edited | edit title/price | — | item keeps original |
| 4 | Edge | Re-price at checkout | cart line price stale (product price changed after add) | valid | 303 | order uses **DB** price 🚩 |
| 5 | Edge | Product unpublished after add | archived | valid | **409** 🚩 | no order, stock unchanged |
| 6 | Edge | Empty cart | — | valid | **400** 🚩 | `ErrEmptyCart` |
| 7 | Edge | Out of stock | qty > stock | valid | **409** 🚩 | no order, stock unchanged (rollback) |
| 8 | Boundary | qty == stock | — | valid | 303 | stock 0 |
| 9 | Validation | Missing field | no email | — | 400 | form re-rendered with error, no order |
| 10 | Validation | Invalid email | `notanemail` | — | 400 | |
| 11 | State | Double submit | same cart twice | ×2 | 303 then 400 | cart cleared on first |
| 12 | Security | Total tampering | hidden `total` field | — | 303 | server recomputes |
| 13 | Security | CSRF missing | — | — | 403 | |
| 14 | Method | Wrong verb | — | `DELETE` | 404 (fallback) | |

#### `GET /shop/orders/{number}`

| # | Type | Scenario | State | Request | Status | Result |
|---|---|---|---|---|---|---|
| 1 | Happy | View | order `N` | `GET …/N` | 200 | items, totals, status |
| 2 | Not found | Unknown | — | `GET …/ZZZ` | **404** | |
| 3 | Security | Guest with number | not logged in, someone else's number | `GET …/<other>` | 200 🚩 | number is the guard; no enumeration |
| 4 | Security | Authed, not owner | logged in, order belongs to another user | `GET …/<other>` | **404** 🚩 | scoped; never 403 |
| 5 | Method | Wrong verb | — | `POST …/N` | 404 (fallback) | |

### 10.5 Admin CMS

**Access-control matrix** — assert for **every** `/shop/admin*` route including the two exact
patterns (`/shop/admin`, `/shop/admin/`), via a table over all routes:

| # | Scenario | State | Status | Result |
|---|---|---|---|---|
| 1 | Unauthenticated browser | no session, `Accept: text/html` | 302 | `Location: /login?next=…`; handler never runs |
| 2 | Unauthenticated API | `Accept: application/json` | 401 | |
| 3 | Invalid session cookie | garbage cookie | 302 / 401 | per negotiation |
| 4 | Role `user` | logged in | 403 | |
| 5 | Role `admin` | logged in | 2xx / 3xx | proceeds |
| 6 | User row deleted | valid cookie, row gone | 302 / 401 | middleware re-checks |
| 7 | Admin POST without CSRF | admin session | 403 | |

Per-endpoint tables assume an admin session **with** a valid `csrf_token`.

#### `GET /shop/admin` · `GET /shop/admin/products` · `GET /shop/admin/products/new`

| # | Type | Endpoint | Scenario | Status | Result |
|---|---|---|---|---|---|
| 1 | Happy | `/shop/admin` | dashboard | 200 | counts + links |
| 2 | Edge | `/shop/admin/` | trailing slash | 301 → `/shop/admin` | still guarded (matrix row 1 applies) |
| 3 | Happy | `/products` | all statuses | 200 | draft+published+archived, edit/delete links |
| 4 | Happy | `/products` | empty | 200 | empty state |
| 5 | Happy | `/products/new` | blank form | 200 | `action=/shop/admin/products`, `csrf_token` |
| 6 | Method | all | wrong verb | 404 (fallback) | |

#### `POST /shop/admin/products`

| # | Type | Scenario | Request | Status | Result |
|---|---|---|---|---|---|
| 1 | Happy | Create published | `title,price=12.99,status=published,sku,stock_qty,description` | 303 → list | row; slug from title; `price_cents=1299` |
| 2 | Happy | Create draft | `status=draft` | 303 | |
| 3 | Boundary | Free item | `price=0` | 303 | `price_cents=0` |
| 4 | Validation | Missing title | — | 400 | form re-rendered, no row |
| 5 | Validation | Bad price | `abc`, `-5` | 400 | |
| 6 | Validation | Bad status | `status=banana` | 400 | |
| 7 | Edge | Slug collision | title maps to existing slug | 303 | `-2` suffix 🚩 |
| 8 | Security | XSS | `title=<script>` | 303 | stored raw, escaped on render |
| 9 | Method | Wrong verb | `PUT` | 404 (fallback) | |

#### `GET /shop/admin/products/{id}/edit`

| # | Type | Scenario | Request | Status | Result |
|---|---|---|---|---|---|
| 1 | Happy | Prefilled | `…/1/edit` | 200 | values, images, `csrf_token` |
| 2 | Not found | Unknown | `…/999/edit` | 404 | |
| 3 | Boundary | Non-numeric / zero | `…/abc/edit`, `…/0/edit` | 404 | `parseID` rejects |
| 4 | Method | Wrong verb | `POST …/1/edit` | 404 (fallback) | |

#### `POST /shop/admin/products/{id}`

| # | Type | Scenario | Request | Status | Result |
|---|---|---|---|---|---|
| 1 | Happy | Update | fields | 303 | row updated |
| 2 | State | Publish draft | `status=published` | 303 | visible on storefront |
| 3 | State | Archive | `status=archived` | 303 | hidden; `GET /shop/products/{slug}` → 404 |
| 4 | Not found | Unknown | `…/999` | 404 | |
| 5 | Validation | Bad price | `-1` | 400 | unchanged |
| 6 | Method | Wrong verb | `DELETE` | 404 (fallback) | |

#### `POST /shop/admin/products/{id}/delete`

| # | Type | Scenario | Request | Status | Result |
|---|---|---|---|---|---|
| 1 | Happy | Delete | `…/1/delete` | 303 | soft-deleted; gone from lists and storefront |
| 2 | Not found | Unknown | `…/999/delete` | 404 | |
| 3 | State | Twice | ×2 | 303 then 404 | |
| 4 | Edge | Referenced by orders | product with order lines | 303 | order lines unchanged |
| 5 | Method | Wrong verb | `GET` | 404 (fallback) | |

#### `POST /shop/admin/products/{id}/images`

| # | Type | Scenario | Request | Status | Result |
|---|---|---|---|---|---|
| 1 | Happy | PNG | multipart `image` | 303 → edit | file under `public/products/`, row with `/public/products/…` URL |
| 2 | Validation | No file | — | 400 | |
| 3 | Validation | Not an image (sniffed) | `.exe` bytes with `Content-Type: image/png` | 400 | header ignored, sniff rejects |
| 4 | Boundary | > 10 MB, `Content-Length` set | — | **413** | rejected by `maxBody` before `CSRF()` reads the body |
| 4b | Boundary | > 10 MB, chunked | no `Content-Length` | 403 | `MaxBytesReader` fails the form parse inside `CSRF()`; accepted trade-off (§4.3) |
| 5 | Not found | Unknown product | `…/999/images` | 404 | |
| 6 | Security | Filename traversal | `filename=../../x.png` | 303 | stored name is server-generated |
| 7 | Method | Wrong verb | `GET` | 404 (fallback) | |

#### `GET` / `POST /shop/admin/categories`

| # | Type | Scenario | Request | Status | Result |
|---|---|---|---|---|---|
| 1 | Happy | List + form | `GET` | 200 | |
| 2 | Happy | Create | `name,description` | 303 | slug from name |
| 3 | Edge | Nested | `parent_id=1` | 303 | |
| 4 | Validation | Missing name | — | 400 | |
| 5 | Edge | Duplicate slug | — | 303 | `-2` suffix |
| 6 | Validation | Unknown `parent_id` | `999` | 400 🚩 | |
| 7 | Security | XSS | `name=<x>` | 303 | escaped on render |
| 8 | Method | Wrong verb | `PUT` | 404 (fallback) | |

#### `GET /shop/admin/orders` · `GET /shop/admin/orders/{number}`

| # | Type | Scenario | Request | Status | Result |
|---|---|---|---|---|---|
| 1 | Happy | List | `GET` | 200 | number, status, total, email |
| 2 | Happy | Empty | — | 200 | |
| 3 | Happy | Filter | `?status=paid` | 200 | filtered 🚩 (implemented) |
| 4 | Happy | Detail | `…/N` | 200 | snapshots, totals, shipping, status form |
| 5 | Not found | Unknown | `…/ZZZ` | 404 | |
| 6 | Method | Wrong verb | `PUT …/N` | 404 (fallback) | |

#### `POST /shop/admin/orders/{number}/status`

| # | Type | Scenario | State | Request | Status | Result |
|---|---|---|---|---|---|---|
| 1 | Happy | Mark paid | pending | `status=paid` | 303 → detail | `paid_at` set |
| 2 | Happy | Fulfil | paid | `status=fulfilled` | 303 | |
| 3 | Happy | Cancel pending | pending | `status=cancelled` | 303 | **restocked** 🚩 |
| 4 | Happy | Cancel paid | paid | `status=cancelled` | 303 | restocked |
| 5 | State | Same status | pending | `status=pending_payment` | 303 | no-op |
| 6 | State | Illegal | paid → pending; fulfilled → cancelled | — | 400 | unchanged |
| 7 | Validation | Bad value | `banana` | 400 | |
| 8 | Not found | Unknown | `…/ZZZ/status` | 404 | |
| 9 | Security | CSRF missing | — | 403 | |
| 10 | Method | Wrong verb | `GET` | 404 (fallback) | |

### 10.6 Cross-cutting & routing

| # | Scenario | Request | Status | Result |
|---|---|---|---|---|
| 1 | Unknown shop path hits the shop fallback, not the site catch-all | `GET /shop/nonexistent` | **404** 🚩 | shop not-found page; proves `/shop/` fallback + exact landing (§4.3) |
| 2 | Unknown admin path is still guarded | `GET /shop/admin/nonexistent` unauth / as `user` / as admin | 302 or 401 / 403 / **404** | `AdminMiddleware` runs before the guarded fallback; existence of admin routes never leaks to anonymous probes |
| 3 | Wrong verb hits the shop fallback, not the site catch-all | `DELETE /shop/products` | **404** | shop not-found page |
| 4 | Site catch-all untouched | `GET /no-such-page` | 200 | `test/mount_seam_test.go` row still passes |
| 5 | Landing is exact, not a subtree | `GET /shop/` | 301 → `/shop` | and `GET /shop` → 200 |
| 6 | Nav link | `GET /shop` | 200 | header contains `href="/shop"` |
| 7 | Shell integrity | `GET /shop` | 200 | `csrf-token` meta, `main.css` link |
| 8 | User menu | logged in, `GET /shop` | 200 | shell renders the user menu (proves `OptionalAuthMiddleware` reaches shop routes) |
| 9 | `sid` cookie attributes | first `POST /shop/cart/add` | 303 | HttpOnly, Path=/, SameSite=Lax |
| 10 | Every form carries the token | `GET` cart / checkout / admin forms | 200 | hidden `csrf_token` present |

### 10.7 Coverage checklist before merge

- [ ] Every route in §4.3 has a happy row and a wrong-verb (404 via fallback) row.
- [ ] Every `/shop/admin*` route (including the two exact patterns) passes the access-control matrix.
- [ ] Cart lifecycle: add → merge → update → remove → cookie creation → login merge.
- [ ] Checkout: success (stock ↓, cart cleared), re-price, unavailable, empty, out-of-stock rollback, validation, double submit.
- [ ] Status transitions incl. restock on cancel.
- [ ] Every 🚩 assertion matches §12.

---

## 11. Phased milestones

Each phase is one PR with an exit criterion. Phase 0 is deliberately tiny: it is the shop's
entire conflict surface with the parallel booking work, so it lands first and rebases cheaply.

0. **Seams** — first clear the two `master` defects in §1 if the parallel work has not
   (`scheduler.go` build break; `MigrateCmd` unregistered). Then
   `internal/server/shop/{api.go,models.go}` with an `API` exposing only `Landing` and
   `NotFound`, and an empty `Models()`; `routes/shop.go` with `MountShop` registering
   `GET /shop`, `GET /shop/{$}` → 301, and the `/shop/` fallback; the one-liners in
   `server.go`, `database.go`, `nav.go`. **Exit:** `go build ./...` passes; `GET /shop` renders
   through the shell with the user menu; `GET /shop/` → 301; `GET /shop/nonexistent` → 404;
   `test/shop_mount_test.go` pins those (a new file, so the parallel work's edits to
   `mount_seam_test.go` never conflict); `clair migrate` is a runnable command. (If
   `internal/server/routes/` has not landed, ship a games-style `shop.Mount` and move it in
   the PR that lands the package.)
1. **Data + domain** — the six models, `Models()`, `Catalog`, `FormatCents`, `Slugify`/`UniqueSlug`,
   `Cart` + both stores, `PlaceOrder`, `Transition`, `MigrateGuest`, `ManualProvider`; unit tests
   §10.1. No UI beyond Phase 0's placeholder.
2. **Storefront reads** — landing, product list (search, category, pagination), product detail,
   category page; `shop.Seed` wired into `clair seed`; Tailwind `@source`. Tests §10.2, §10.6.
3. **Cart** — `CartStore` wired in `NewAPI`, add/update/remove/view, guest migrator registered,
   cart-count badge in the header (Alpine, optional). Tests §10.3.
4. **Checkout** — form, validation, `PlaceOrder`, cart clear, confirmation page, owner scoping.
   Tests §10.4.
5. **Admin CMS** — guarded `/shop/admin/` fallback, product CRUD, upload (`maxBody` outside
   `CSRF()`), categories, orders list/detail/status. Tests §10.5.
6. **Polish** — empty states, pagination links, `?status=` filter, `.gitignore`, coverage
   checklist §10.7.

Later / additive, all inside `shop_*`: `StripeProvider` + `ShopPayment` + `/webhooks/shop/{provider}`;
`ImageStore` on S3; `SweepCarts` on the scheduler; multi-vendor (`ShopVendor`, per-vendor
scoping); discounts; stock reservations (`ShopReservation`) if oversell at checkout ever becomes
a real problem.

---

## 12. Decisions (locked)

| Question | Decision |
|---|---|
| Kernel reuse | **No.** Independent `shop_*` order/stock/payment. Rejected reuse 2026-09-02. |
| Model location | `internal/database/models/shop_*.go`, per master convention. |
| Route shape | `routes/shop.go` + `shop.NewAPI`; games-style `Mount` only as a stopgap. |
| Rendering | Server forms + PRG; Alpine optional. |
| Guest vs login checkout | **Guest allowed**; `OwnerRef` owns cart and order. |
| Cart key | `OwnerRef` from day one (`OptionalAuthMiddleware` is global). |
| Guest cart on login | **Merge** via `RegisterGuestMigrator("shop", …)`; pending guest orders re-pointed. |
| Cart prices | Display cache only; **checkout re-prices from the database** and refuses unpublished lines (409). |
| Canonical shop URL | **Bare `/shop`** and `/shop/admin`; trailing-slash forms 301 to them. Reason: exact ServeMux patterns; a `/shop/` subtree pattern would swallow unknown paths. |
| Not found | Real `404` status + `pages.NotFound()` body, for unknown product/category/order and unknown `/shop/*` paths. |
| Order number | `SecureToken()`; guest view guarded by the number alone; authed non-owner → 404. |
| Slug collision | `UniqueSlug` suffixes `-2`, `-3`, …; `uniqueIndex` remains the backstop. |
| Order status | State-machine map (§3.8); illegal → 400; cancel of pending/paid **restocks**; fulfilled is terminal. |
| Image upload | ≤ 10 MB via `maxBody` middleware **outside** `CSRF()` (413; chunked oversize → 403); type by **sniff only**; server-generated filenames. |
| Wrong verb on a shop route | **404** from the shop's subtree fallback, never 405: the site's method-less `/` catch-all makes ServeMux's 405 path unreachable. Bare `POST /shop` reaches the site catch-all (200) and is not asserted. |
| Unknown category filter | 200 + empty list (consistent with search). Unknown category **page** → 404. |
| Roles | `user` / `admin` from the central layer. |
| Currency | USD only at launch. |
| Empty cart on `POST /checkout` | 400. Out of stock / unavailable: 409. |
| Missing `product_id` on cart update/remove | 400. |
| Unknown `parent_id` on category create | 400. |
| Admin orders `?status=` filter | Implemented in Phase 5 (cheap, one `Where`). |

Left open, decide at build time: security headers (CSP, `X-Content-Type-Options`) — site-wide,
not the shop's; shipping cost (fixed 0 in `ShippingCents` until a rule exists).

---

## 13. Coordination with the parallel booking work

The shop only touches four shared files, each with one added line (`server.go`, `database.go`,
`nav.go`, `seed.go`). Everything else is new files. Two things to watch at rebase time:

- **`internal/server/routes/` and `booking.NewAPI`** are in the parallel tree, not yet on
  `master`. The shop's route table assumes them. If they land first, nothing changes; if the shop
  lands first, Phase 0 ships a `shop.Mount` and the move is a rename.
- **`clair seed`** does not exist on `master`; the parallel tree adds it for the Aurenor
  catalog. If it has landed, adding `shop.Seed` is one call inside its `RunE` (the `Report`
  logging is the only conflict-prone line); if not, the shop ships `clair seed-shop` (§3.10).
- **`MigrateCmd` and the `scheduler.go` build break** (§1) are both fixed in the parallel tree's
  `root.go` / `scheduler.go`. Whoever lands first carries the fix.

Nothing in the shop imports `internal/booking`, `internal/appointments`, `internal/ticketing`
or `internal/server/booking`, and nothing there imports the shop.

---

## 14. Changes from the previous revision (2026-09-02)

Re-checked every claim against `master` at `a1b3215`. Corrections:

| Was | Now | Why |
|---|---|---|
| "Soft-auth is a gap; key the cart on raw `sid` in the interim; raise upstream" (§7.3, §7.8, Phase 0) | Cart keyed on `OwnerRef` from day one | `OptionalAuthMiddleware` exists and is mounted on every app route (`server.go`). The gap is closed. |
| Guest cart merge "after sign-in or lazily on next access" (§7.4) | `api_auth.RegisterGuestMigrator("shop", …)` in `NewAPI` | The hook exists; booking already uses it. |
| Models in `internal/shop/models` aliased `sm`; booking said to use `bm/am/tm` packages | `internal/database/models/shop_*.go`, one file per model | That is the convention `database.go` documents and booking actually follows. The `bm/am/tm` claim was never true on `master`. |
| Register models "inside `AutoMigrate` in `database.New`" | `shop.Models()` appended in `MigrationModels()`; `clair migrate` is explicit | PR #18 made migration an explicit step; `database.New` migrates only when `DATABASE_AUTO_MIGRATE` is truthy. (`MigrateCmd` is not yet registered in `root.go` on `master` — §1.) |
| 404 for unknown `/shop/*` and 405 for wrong verbs, asserted against the real router | Shop-owned `/shop/` and `/shop/admin/` subtree fallbacks answering 404; wrong verb = 404; no 405 rows | The site's method-less `/` catch-all returns 200 for everything unmatched and makes ServeMux's 405 path unreachable; the previous tables would have failed on every such row. Registered on a real mux during review. |
| `MaxBytesReader` inside the upload handler | `maxBody` middleware mounted outside `CSRF()` | `CSRF()` parses the multipart body (`PostFormValue`) before the handler runs, so a cap in the handler never sees the body. |
| Guest migrator closure capturing the `API`'s cart store | `sync.Once` registration, closure captures only the Valkey client, store built from the `db` argument | The registry is append-only with no dedup and every `Routes()` call builds a new `API`; tests would register N migrators over N stale stores. |
| "`clair seed` seeds Aurenor" stated as fact | Marked parallel-work only; `seed-shop` fallback | Neither `internal/cmd/seed.go` nor `internal/aurenor/` exists on `master`. |
| `foreignKey:` "same technique as `BookingSlot`"; `gorm.Model` "like booking" | Both marked new for this domain | No booking model declares a GORM association or uses `gorm.Model`; nothing on `master` calls `Preload`. |
| Route group inlined into `server.go` | `routes.MountShop` + `shop.NewAPI`, one line in `Routes()` | `master` mounts one line per domain; the parallel work moves the table to `routes/`. |
| `web.Base("Not found", web.NotFound())` | `web.Base(api_auth.PageShell(w, r, title), pages.NotFound())` | The real signatures. `PageShell` is what mints the CSRF token and fills the user menu. |
| Thread `CSRFToken(r)` into views | Use `shell.CSRFToken` from the `PageShell` already built; hidden `csrf_token` field | `CSRFToken(r)` alone does not persist a fresh token; `PageShell` does. |
| Nav via `@NavItem` in `header.templ` | One entry in `components.NavLinks` | `nav.go` is the single source of truth now. |
| testcontainers `TestMain` harness | Existing `DATABASE_URL` harness: `newRoutesServer`, `newAuthTestDB`, `createUser`, `jarClient`, `csrfMetaToken`, `testsupport.PostgresDB` | `master` has no testcontainers dependency and already has everything the tables need. |
| Canonical `/shop/` with 301 from `/shop`; landing registered as `"GET /"` in the group | Canonical **`/shop`**; landing registered exactly (`GET /shop`, `GET /shop/{$}` → 301) | A `/shop/` pattern is a ServeMux subtree and would answer `/shop/anything` with the landing page instead of a 404 (the booking route table documents the same trap). |
| 404 page implicit | Real `404` status via `notFound()` | The site's catch-all returns 200 for unknown paths; the shop should not inherit that for product/order lookups. |
| `title LIKE ?` | `ILIKE` | Postgres `LIKE` is case-sensitive. |
| Stock decrement ignored `res.Error`; cart prices trusted at checkout | `PlaceOrder` checks errors, re-prices from DB, refuses unpublished lines | The cart is a display cache; money comes from the database. |
| Cancel-restock "🚩 consider" | Cancel of pending/paid restocks; fulfilled terminal | Otherwise a cancelled order leaks stock forever. |
| Upload: client `Content-Type` + `filepath.Base(hdr.Filename)` | Sniff only; server-generated names | Client header is untrusted; a generated name removes the traversal question entirely. |
| `ShopCart.ID` = `sid` | `ShopCart.OwnerRef` | The key is the owner reference, not the raw session id. |
| §2.1 cited `docs/booking-system-design.md`, branch `docs/booking-system-design`, target branch `claude/optimistic-rubin-wpbnbm` | Removed | None of those exist on `master`. |
| Phase 0 "consume auth" | Phase 0 "seams" (four one-liners + empty `API`) | Auth needs no phase; the conflict-prone lines do. |

Kept unchanged: locked storefront/CMS bases, single tenancy, mock payments with a provider
seam, `Shop*`/`shop_*` naming, guest checkout, merge-on-login, USD, `SecureToken` order
numbers, slug suffixing, state-machine transitions, upload limits, unknown-category-filter
semantics, the per-endpoint test matrices (rows corrected where the decisions above changed).

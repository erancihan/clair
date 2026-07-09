# Shop Front System — Implementation Plan

Status: **Design / not yet implemented**
Target branch: `claude/optimistic-rubin-wpbnbm`

## 0. Locked decisions

| Decision | Choice |
|---|---|
| Storefront base | `/shop` |
| CMS base | `/shop/admin` (behind auth middleware + admin role) |
| Tenancy | Single store (one owner). Multi-vendor is a later, additive layer. |
| Payments | Mock/manual now — orders created as `pending_payment`, with a clean `PaymentProvider` seam for Stripe later. |

Everything below is written to match the existing idioms in this repo:
`templ` views + `web.Base()` shell, the custom `router.Router` (`Group` / `Middleware`),
the `GameService`-style handler factories (`func(ctx) http.HandlerFunc`), GORM models (the shared
`User` in `internal/database/models`; shop models prefixed `Shop*` in their own
`internal/shop/models`, §2.1), and Valkey via `ctx.ValKey`.

---

## 1. How it fits the existing architecture

New/changed packages (mirrors the `games` split of domain vs. HTTP):

```
internal/
├── database/models/
│   └── users.go             # CHANGE: add Role field for admin gating (shared, bare `users`)
├── shop/                    # NEW: domain layer (pure logic, no net/http)
│   ├── models/              # NEW: Shop* models, package `models`, aliased `sm` (tables shop_*)
│   │   └── shop.go          #   ShopCategory, ShopProduct, ShopProductImage, ShopOrder, ShopOrderItem
│   ├── catalog.go           #   product/category queries & CRUD
│   ├── cart.go              #   Cart type + CartStore interface
│   ├── cart_valkey.go       #   Valkey-backed CartStore
│   ├── cart_db.go           #   DB-backed CartStore (fallback when Valkey is nil)
│   ├── checkout.go          #   cart -> order conversion
│   ├── money.go             #   int64 minor-units + formatting
│   └── slug.go              #   slugify helper
├── server/shop/             # NEW: HTTP layer (handler factories, like server/games)
│   ├── service.go           #   ShopService struct wiring domain + views
│   ├── storefront.go        #   GET /shop/* public handlers
│   ├── cart.go              #   cart add/update/remove/view
│   ├── checkout.go          #   checkout page + submit
│   └── admin.go             #   /shop/admin CRUD handlers (protected)
└── web/pages/shop/          # NEW: templ views, package `shopviews`
    ├── storefront.templ     #   landing, product list, product detail, category
    ├── cart.templ
    ├── checkout.templ
    └── admin.templ          #   dashboard, product form, order list/detail
```

Domain (`internal/shop`) never imports `net/http`; HTTP handlers (`internal/server/shop`)
translate requests, call domain, and render `shopviews.*` through `web.Base(...)`.
This is the same separation you already have between `internal/games/*` and
`internal/server/games/*`.

---

## 2. Data model (GORM)

### 2.1 Coexistence & name-collision audit (booking domain)

A sibling design — `docs/booking-system-design.md` (branch `docs/booking-system-design`) — adds a
booking/ticketing system to the **same single Postgres database**. It was **explicitly authored to
coexist with this shopfront**: every booking type is prefixed `Booking*` (GORM → tables `booking_*`).

**Decision (this plan): the shop mirrors that convention.** Every shop model is prefixed `Shop*`
(GORM → tables `shop_*`) and lives in its own `internal/shop/models` package (aliased `sm`), exactly
paralleling booking's `bm` / `am` / `tm`. We deliberately go beyond the booking doc's assumption
(it expected the shop to take the bare `orders` / `order_items` / `inventories` / `payments` names) —
prefixing both domains keeps every table grep-attributable to its owner and leaves the bare generic
names unclaimed. The booking doc picks the type-prefix approach over a scoped `NamingStrategy`
precisely for this grep-ability; we match it so the two domains read as siblings. **No collisions on
either axis:**

- **Tables.** Shop owns the `shop_*` namespace (`shop_products`, `shop_categories`,
  `shop_product_images`, `shop_orders`, `shop_order_items`). Booking owns `booking_*`
  (`booking_orders`, `booking_inventories`, `booking_payments`, `booking_seats`, …). Disjoint prefixes.
- **Go types.** Shop models live in `internal/shop/models` (`package models`, alias `sm`); booking
  models in `internal/{booking,appointments,ticketing}/models` (aliased `bm` / `am` / `tm`). Shop's
  `sm.ShopOrder` / `sm.ShopOrderItem` and booking's `bm.BookingOrder` / `bm.BookingOrderItem` are
  distinct identifiers in distinct import paths. The only shared model is `User`.

**Authoritative table-namespace split:**

| Domain | Models package | Table prefix | Example tables |
|---|---|---|---|
| Shop (this plan) | `internal/shop/models` (alias `sm`) | `shop_` | `shop_products`, `shop_categories`, `shop_product_images`, `shop_orders`, `shop_order_items` |
| Booking kernel / appointments / ticketing | `internal/{booking,appointments,ticketing}/models` | `booking_` | `booking_orders`, `booking_inventories`, `booking_payments`, `booking_seats`, … |
| Shared | `internal/database/models` | *(bare)* | `users` |

**Ownership split (reconciled with the booking design).** The shop ships first and **owns the shared
infrastructure**; booking owns its reservation kernel and consumes the shared pieces:

| Shared piece | Owner | Where | Consumed by |
|---|---|---|---|
| `User.Role` + `AdminMiddleware` | **Shop** | `internal/server/authentication` (§7.1) | booking's `/admin/*` reuse it verbatim |
| Request-context identity + `CurrentUser(ctx)` | **Shop** | `internal/server/authentication` (§7.2) | every authed handler in both domains |
| `sid` session/guest id + `OwnerRef` | **Shop** | `internal/server/authentication` (§7.3) | booking's guest `OwnerRef` (its only "who is this") |
| `newPostgresConn` + driver-switch fix | **Shop** | `internal/database/database.go` (§2.3) | both |
| Merged `AutoMigrate` + `ifPostgres` guard | **Shop** | `internal/database/database.go` (§2.3) | booking's Postgres-only DDL wraps in `ifPostgres` |
| App-wide CSRF + session hardening | **Shop** | `internal/server/authentication` (§7.4–7.5) | booking's `/hold` · `/checkout` · `/cancel` |
| Reservation kernel (`BookingInventory`/`Hold`/`Payment`, holds/TTL, capture-time commit, seats, waitlist, reaper) | **Booking** | `internal/booking` etc. | — shop does **not** build or touch these |

**Coordination points (shared surfaces to align with the booking work):**

1. **One database, one driver — PostgreSQL (decided).** The shop and booking share a single
   `*gorm.DB` on Postgres. `internal/database/database.go` completes `newPostgresConn`
   (`gorm.io/driver/postgres`, `DATABASE_URL`) and fixes the latent `case "sqlite"` bug (§2.3); the
   shop no longer rides the SQLite default in production. Shop models use only portable GORM tags and
   the `Shop*` type-prefix derives `shop_*` identically on any driver, so this is a config change,
   not a schema rewrite. Testing: in-memory SQLite stays valid for fast shop-only unit/endpoint tests
   (no Postgres-only features used); the shared/cross-domain integration suite runs on Postgres
   (testcontainers); see §10.1.
2. **Unified `AutoMigrate` + `ifPostgres` guard (§2.3).** One merged list (`User` once, `sm.*` shop
   models, then booking's `bm.*`/`am.*`/`tm.*`). Booking's Postgres-only DDL runs inside the shop's
   `ifPostgres` helper so the SQLite harness stands up cleanly.
3. **`User.Role` + `AdminMiddleware` — shop-owned, resolved.** The shop adds `Role`
   (`RoleCustomer`/`RoleAdmin`, string type for headroom) to the shared `User` and owns the single
   `AdminMiddleware` + `CurrentUser`/`OwnerRef` helpers in `internal/server/authentication` (§7).
   Booking reuses them; nobody builds a second role system.
4. **Routes are already disjoint.** Shop: `/shop/*`, `/shop/admin/*`. Booking: `/appointments/*`,
   `/events/*`, `/admin/*`, `/webhooks/*`. This is exactly why we picked `/shop/admin` over
   `/admin/shop` — it keeps the shop out of booking's top-level `/admin` group. Keep it that way.
5. **Forward-looking (shop's §11 "later" features).** When the shop adds real payments or stock
   reservations, keep them inside the `shop_*` namespace (`ShopPayment` → `shop_payments`,
   `ShopInventory` → `shop_inventories`) and never reuse `booking_*`. Note that shop "inventory"
   (product stock) is a *different concept* from `BookingInventory` (a seat/slot capacity pool);
   today the shop sidesteps it entirely with a plain `ShopProduct.StockQty` column. If shop and
   booking payments ever need shared logic, extract a provider interface — don't share a table.

### 2.2 Models

`internal/shop/models/shop.go` — `package models`, imported everywhere else in the shop as `sm`.
Every type carries the `Shop` prefix so GORM derives `shop_*` table names (§2.1). Money is stored as
**int64 minor units** (cents) plus a currency code — never floats. Order line items snapshot
title/price so historical orders don't change when a product is later edited.

> **Why the `foreignKey:` tags.** For the **has-many** sides (`Images []ShopProductImage`,
> `Items []ShopOrderItem`) the `Shop*` type prefix breaks GORM's default FK inference — it would look
> for `ShopProductID` / `ShopOrderID` columns — so `foreignKey:ProductID` / `foreignKey:OrderID` are
> required to keep columns short (`product_id`, `order_id`); this is the same technique the booking
> models use (`foreignKey:SlotID`). On the **belongs-to** `Category *ShopCategory` the tag is merely
> explicit — GORM already infers `CategoryID` from the field name.

```go
package models // import path: github.com/erancihan/clair/internal/shop/models (aliased sm)

import "gorm.io/gorm"

// ----- Catalog -----

type ShopProductStatus string

const (
	ShopProductDraft     ShopProductStatus = "draft"
	ShopProductPublished ShopProductStatus = "published"
	ShopProductArchived  ShopProductStatus = "archived"
)

type ShopCategory struct {
	gorm.Model
	Slug        string `json:"slug"        gorm:"uniqueIndex;not null"`
	Name        string `json:"name"        gorm:"not null"`
	Description string `json:"description"`
	ParentID    *uint  `json:"parent_id"   gorm:"index"` // self-referential tree
	Position    int    `json:"position"    gorm:"default:0"`
}

type ShopProduct struct {
	gorm.Model
	Slug        string            `json:"slug"        gorm:"uniqueIndex;not null"`
	Title       string            `json:"title"       gorm:"not null"`
	Description string            `json:"description"`
	Status      ShopProductStatus `json:"status"      gorm:"index;default:draft"`
	PriceCents  int64             `json:"price_cents" gorm:"not null"` // minor units
	Currency    string            `json:"currency"    gorm:"default:USD"`
	SKU         string            `json:"sku"         gorm:"index"`
	StockQty    int               `json:"stock_qty"   gorm:"default:0"`
	Featured    bool              `json:"featured"    gorm:"index;default:false"`

	CategoryID *uint              `json:"category_id" gorm:"index"`
	Category   *ShopCategory      `json:"category,omitempty" gorm:"foreignKey:CategoryID"`
	Images     []ShopProductImage `json:"images,omitempty"   gorm:"foreignKey:ProductID"`
}

type ShopProductImage struct {
	gorm.Model
	ProductID uint   `json:"product_id" gorm:"index;not null"` // -> shop_products.id
	URL       string `json:"url"        gorm:"not null"`
	Alt       string `json:"alt"`
	Position  int    `json:"position"   gorm:"default:0"`
}

// ----- Orders -----

type ShopOrderStatus string

const (
	ShopOrderPendingPayment ShopOrderStatus = "pending_payment"
	ShopOrderPaid           ShopOrderStatus = "paid"
	ShopOrderFulfilled      ShopOrderStatus = "fulfilled"
	ShopOrderCancelled      ShopOrderStatus = "cancelled"
)

type ShopOrder struct {
	gorm.Model
	Number   string          `json:"number" gorm:"uniqueIndex"` // high-entropy, unguessable — sole IDOR guard for guest view (§12)
	UserID   *uint           `json:"user_id" gorm:"index"`      // nullable => guest checkout; -> users.id
	Email    string          `json:"email"   gorm:"index"`
	Status   ShopOrderStatus `json:"status"  gorm:"index;default:pending_payment"`
	Currency string          `json:"currency"`

	SubtotalCents int64 `json:"subtotal_cents"`
	ShippingCents int64 `json:"shipping_cents"`
	TotalCents    int64 `json:"total_cents"`

	// snapshot of shipping details at purchase time
	ShipName    string `json:"ship_name"`
	ShipAddress string `json:"ship_address"`
	ShipCity    string `json:"ship_city"`
	ShipPostal  string `json:"ship_postal"`
	ShipCountry string `json:"ship_country"`

	Items []ShopOrderItem `json:"items,omitempty" gorm:"foreignKey:OrderID"`
}

type ShopOrderItem struct {
	gorm.Model
	OrderID   uint   `json:"order_id"  gorm:"index;not null"` // -> shop_orders.id
	ProductID uint   `json:"product_id" gorm:"index"`         // -> shop_products.id (may be soft-deleted later)
	// immutable snapshot — do NOT join back to ShopProduct for display
	Title     string `json:"title"`
	SKU       string `json:"sku"`
	UnitCents int64  `json:"unit_cents"`
	Quantity  int    `json:"quantity"`
	LineCents int64  `json:"line_cents"` // UnitCents * Quantity
}
```

Register the shop models in the `AutoMigrate` call of **whichever `database.New` path is active**.
Today the only working registration is `db.AutoMigrate(&models.User{})` inside `newSQLiteConn`
(`internal/database/database.go`), so add the shop models there for the dev/SQLite server **and** in
`newPostgresConn` once it's completed for the shared production DB (§2.1) — one merged list per path,
alongside the shared `User` and the booking models:

```go
import (
	"github.com/erancihan/clair/internal/database/models"      // shared: User
	sm "github.com/erancihan/clair/internal/shop/models"       // shop: Shop* -> shop_*
	// bm/am/tm "…/internal/{booking,appointments,ticketing}/models" // booking work
)

db.AutoMigrate(
	&models.User{},          // shared, registered once
	&sm.ShopCategory{},
	&sm.ShopProduct{},
	&sm.ShopProductImage{},
	&sm.ShopOrder{},
	&sm.ShopOrderItem{},
	&sm.ShopCart{}, // DB-backed cart fallback (§3.3); table shop_carts
	// … booking models registered by the booking work (bm.*, am.*, tm.*)
)
```

### 2.3 Database wiring & the migration guard (shop-owned)

The shop completes the DB layer both domains ride on. Three pieces (brief A3/A4):

**(a) `newPostgresConn` + fix the driver switch.** Today `database.New` has a latent bug: `case
"sqlite":` has an empty body and Go does not fall through, so `DB_DRIVER=sqlite` returns `nil, nil`
(a nil DB); only an *unset* `DB_DRIVER` reaches the `default` and yields a real SQLite DB.

```go
import (
	"time"
	"gorm.io/driver/postgres" // absent today: run `go get gorm.io/driver/postgres`
)

func New(ctx context.Context) (*gorm.DB, error) {
	switch os.Getenv("DB_DRIVER") {
	case "postgres":
		return newPostgresConn(ctx)
	default: // "", "sqlite", or anything else -> SQLite.
		// Fixes the old empty `case "sqlite":` that fell to `return nil, nil`.
		return newSQLiteConn(ctx)
	}
}

func newPostgresConn(ctx context.Context) (*gorm.DB, error) {
	dsn := os.Getenv("DATABASE_URL") // postgres://user:pass@host:5432/clair?sslmode=require
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{PrepareStmt: true})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(time.Hour)
	return db, nil
}
```

**(b) `ifPostgres` dialect guard.** Booking's migrations use Postgres-only DDL (partial-unique
indexes, `CHECK` constraints, `FOR UPDATE` paths). The shop provides one guard so that DDL runs only
on Postgres and the in-memory-SQLite test harness (§10.1) skips it cleanly rather than erroring.

```go
// ifPostgres runs Postgres-only DDL only when the live dialect is postgres.
func ifPostgres(db *gorm.DB, fn func()) {
	if db.Dialector.Name() == "postgres" {
		fn()
	}
}

// booking's Phase-0 migration then wraps its raw DDL:
//   ifPostgres(db, func() {
//     db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_active_bhold ON booking_holds(...) WHERE status='active'`)
//     db.Exec(`ALTER TABLE booking_inventories ADD CONSTRAINT chk_one_owner CHECK (...)`)
//   })
```

**(c) One merged `AutoMigrate` (§2.2)** — registered on whichever connection `New` returns; `User`
once, shop `sm.*`, then booking's `bm.*`/`am.*`/`tm.*`. Because the shop uses only portable tags, the
merged `AutoMigrate` + `ifPostgres` guard let the whole app stand up cleanly on SQLite for dev/tests
and on Postgres in production (contract C, verified in §10.1).

---

## 3. Domain layer (`internal/shop`)

### 3.1 Money (`money.go`)

```go
package shop

import "fmt"

// FormatCents renders minor units as a human string, e.g. (1299, "USD") -> "$12.99".
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

### 3.2 Catalog service (`catalog.go`)

Thin GORM wrappers. Public queries only surface `published` products; admin uses the unfiltered
variants.

```go
package shop

import (
	"context"

	sm "github.com/erancihan/clair/internal/shop/models"
	"gorm.io/gorm"
)

type Catalog struct{ db *gorm.DB }

func NewCatalog(db *gorm.DB) *Catalog { return &Catalog{db: db} }

type ListParams struct {
	CategorySlug string
	Search       string
	Page, PerPage int
}

func (c *Catalog) tx(ctx context.Context) *gorm.DB {
	return c.db.Session(&gorm.Session{Context: ctx})
}

// ListPublished powers /shop/products with basic filter + pagination.
func (c *Catalog) ListPublished(ctx context.Context, p ListParams) ([]sm.ShopProduct, int64, error) {
	if p.PerPage <= 0 {
		p.PerPage = 12
	}
	if p.Page <= 0 {
		p.Page = 1
	}

	q := c.tx(ctx).Model(&sm.ShopProduct{}).Where("status = ?", sm.ShopProductPublished)
	if p.Search != "" {
		q = q.Where("title LIKE ?", "%"+p.Search+"%")
	}
	if p.CategorySlug != "" {
		// NB: real table names are shop_* (§2.1). Prefer the association join so GORM
		// resolves them for you: q.Joins("Category").Where("shop_categories.slug = ?", …).
		q = q.Joins("JOIN shop_categories ON shop_categories.id = shop_products.category_id").
			Where("shop_categories.slug = ?", p.CategorySlug)
	}

	var total int64
	q.Count(&total)

	var products []sm.ShopProduct
	err := q.Preload("Images").
		Offset((p.Page - 1) * p.PerPage).Limit(p.PerPage).
		Order("created_at DESC").Find(&products).Error
	return products, total, err
}

func (c *Catalog) BySlug(ctx context.Context, slug string) (*sm.ShopProduct, error) {
	var p sm.ShopProduct
	err := c.tx(ctx).Preload("Images").Preload("Category").
		Where("slug = ? AND status = ?", slug, sm.ShopProductPublished).
		First(&p).Error
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Admin CRUD (unfiltered by status)
func (c *Catalog) AdminList(ctx context.Context) ([]sm.ShopProduct, error) {
	var ps []sm.ShopProduct
	err := c.tx(ctx).Preload("Images").Order("updated_at DESC").Find(&ps).Error
	return ps, err
}
func (c *Catalog) Create(ctx context.Context, p *sm.ShopProduct) error { return c.tx(ctx).Create(p).Error }
func (c *Catalog) Update(ctx context.Context, p *sm.ShopProduct) error { return c.tx(ctx).Save(p).Error }
func (c *Catalog) Delete(ctx context.Context, id uint) error {
	return c.tx(ctx).Delete(&sm.ShopProduct{}, id).Error
}

// AddImage attaches an uploaded image to a product (used by the admin upload handler, §8).
func (c *Catalog) AddImage(ctx context.Context, productID uint, url, alt string) error {
	return c.tx(ctx).Create(&sm.ShopProductImage{ProductID: productID, URL: url, Alt: alt}).Error
}
```

### 3.3 Cart (`cart.go` + two stores)

The Valkey client can be **nil** (see `utils.NewValKeyClient`), so the cart is behind an interface
and we pick the implementation at wire time. Cart is keyed by the shared `sid` session id (§7.3).

```go
package shop

import "context"

type CartItem struct {
	ProductID uint   `json:"product_id"`
	Slug      string `json:"slug"`
	Title     string `json:"title"`
	SKU       string `json:"sku"` // carried so PlaceOrder can snapshot it onto ShopOrderItem
	UnitCents int64  `json:"unit_cents"`
	Quantity  int    `json:"quantity"`
	ImageURL  string `json:"image_url"`
}

type Cart struct {
	Currency string     `json:"currency"`
	Items    []CartItem `json:"items"`
}

func (c *Cart) SubtotalCents() int64 {
	var sum int64
	for _, it := range c.Items {
		sum += it.UnitCents * int64(it.Quantity)
	}
	return sum
}

func (c *Cart) Count() int {
	n := 0
	for _, it := range c.Items {
		n += it.Quantity
	}
	return n
}

// Upsert adds qty (qty may be negative). Zero/negative total removes the line.
func (c *Cart) Upsert(item CartItem, qty int) {
	for i := range c.Items {
		if c.Items[i].ProductID == item.ProductID {
			c.Items[i].Quantity += qty
			if c.Items[i].Quantity <= 0 {
				c.Items = append(c.Items[:i], c.Items[i+1:]...)
			}
			return
		}
	}
	if qty > 0 {
		item.Quantity = qty
		c.Items = append(c.Items, item)
	}
}

type CartStore interface {
	// Load returns a non-nil Cart (empty if none exists) whenever err == nil,
	// so callers can Upsert without a nil check. A missing key is not an error.
	Load(ctx context.Context, id string) (*Cart, error)
	Save(ctx context.Context, id string, cart *Cart) error
}
```

Valkey implementation (`cart_valkey.go`), using the `valkey-go` builder API:

```go
package shop

import (
	"context"
	"encoding/json"
	"time"

	"github.com/valkey-io/valkey-go"
)

type valkeyCartStore struct{ client valkey.Client }

func NewValkeyCartStore(c valkey.Client) CartStore { return &valkeyCartStore{client: c} }

const cartTTL = 7 * 24 * time.Hour

func (s *valkeyCartStore) key(id string) string { return "cart:" + id }

func (s *valkeyCartStore) Load(ctx context.Context, id string) (*Cart, error) {
	res := s.client.Do(ctx, s.client.B().Get().Key(s.key(id)).Build())
	if err := res.Error(); err != nil {
		if valkey.IsValkeyNil(err) {
			return &Cart{Currency: "USD"}, nil // empty cart
		}
		return nil, err
	}
	raw, _ := res.ToString()
	var cart Cart
	if err := json.Unmarshal([]byte(raw), &cart); err != nil {
		return &Cart{Currency: "USD"}, nil
	}
	return &cart, nil
}

func (s *valkeyCartStore) Save(ctx context.Context, id string, cart *Cart) error {
	data, _ := json.Marshal(cart)
	return s.client.Do(ctx,
		s.client.B().Set().Key(s.key(id)).Value(string(data)).
			Ex(cartTTL).Build(),
	).Error()
}
```

`cart_db.go` mirrors the interface with a `sm.ShopCart` model (table **`shop_carts`** — stays inside
the `shop_*` namespace per §2.1; add it to the merged `AutoMigrate`) so local dev works with Valkey
off:

```go
// internal/shop/models/shop.go
type ShopCart struct {
	ID        string    `gorm:"primaryKey"` // the sid
	Data      string    // JSON-encoded Cart
	UpdatedAt time.Time
}
```

Wire selection:

```go
func NewCartStore(v valkey.Client, db *gorm.DB) CartStore {
	if v != nil {
		return NewValkeyCartStore(v)
	}
	return NewDBCartStore(db)
}
```

### 3.4 Checkout (`checkout.go`)

Converts a cart into a `ShopOrder` in a transaction, snapshotting each line and (optionally)
decrementing stock. This is the seam where a real `PaymentProvider` plugs in.

```go
package shop

import (
	"context"

	sm "github.com/erancihan/clair/internal/shop/models"
	"gorm.io/gorm"
)

type ShippingDetails struct {
	Email, Name, Address, City, Postal, Country string
}

func PlaceOrder(ctx context.Context, db *gorm.DB, cart *Cart, s ShippingDetails, orderNumber string) (*sm.ShopOrder, error) {
	if len(cart.Items) == 0 {
		return nil, ErrEmptyCart
	}

	order := &sm.ShopOrder{
		Number: orderNumber, Email: s.Email, Status: sm.ShopOrderPendingPayment,
		Currency: cart.Currency,
		ShipName: s.Name, ShipAddress: s.Address, ShipCity: s.City,
		ShipPostal: s.Postal, ShipCountry: s.Country,
	}
	for _, it := range cart.Items {
		line := it.UnitCents * int64(it.Quantity)
		order.SubtotalCents += line
		order.Items = append(order.Items, sm.ShopOrderItem{
			ProductID: it.ProductID, Title: it.Title, SKU: it.SKU, UnitCents: it.UnitCents,
			Quantity: it.Quantity, LineCents: line,
		})
	}
	order.TotalCents = order.SubtotalCents + order.ShippingCents

	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(order).Error; err != nil {
			return err
		}
		// optional: decrement stock atomically, guard against oversell
		for _, it := range cart.Items {
			res := tx.Model(&sm.ShopProduct{}).
				Where("id = ? AND stock_qty >= ?", it.ProductID, it.Quantity).
				UpdateColumn("stock_qty", gorm.Expr("stock_qty - ?", it.Quantity))
			if res.RowsAffected == 0 {
				return ErrOutOfStock
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return order, nil
	// NEXT: provider.CreateCheckout(order) -> redirect URL; webhook flips status to Paid.
}
```

`orderNumber` is generated by the HTTP layer with `api_auth.SecureToken()` (high-entropy — it's the
guest IDOR guard, §12) and passed in, so the domain stays deterministic and testable. `PlaceOrder` deliberately takes no `CartStore`: the
`SubmitCheckout` handler owns the cart, so **on success it clears the cart** (`s.Carts.Save(ctx, id,
&shop.Cart{Currency: …})` or a `Delete`) before redirecting to `/shop/orders/{number}`. That is what
the endpoint tests assert (empty cart after checkout; a double-submit then sees an empty cart, §10.5
row 1 / §10.6 double-submit).

---

## 4. HTTP layer (`internal/server/shop`) + router wiring

### 4.1 Service struct (`service.go`)

```go
package shop

import (
	server_context "github.com/erancihan/clair/internal/server/context"
	"github.com/erancihan/clair/internal/shop"
)

type Service struct {
	Catalog *shop.Catalog
	Carts   shop.CartStore
}

func New(ctx server_context.BackEndContext) *Service {
	return &Service{
		Catalog: shop.NewCatalog(ctx.DBConn),
		Carts:   shop.NewCartStore(ctx.ValKey, ctx.DBConn),
	}
}
```

### 4.2 A storefront handler (`storefront.go`)

Handlers follow the existing `func(ctx) http.HandlerFunc` factory shape and render
`shopviews.*` through `web.Base`.

```go
func (s *Service) ProductDetail(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug") // Go 1.22+ path wildcards
		product, err := s.Catalog.BySlug(r.Context(), slug)
		if err != nil {
			templ.Handler(web.Base("Not found", web.NotFound())).ServeHTTP(w, r)
			return
		}
		templ.Handler(
			web.Base(product.Title, shopviews.ProductDetail(product)),
		).ServeHTTP(w, r)
	}
}
```

### 4.3 Add-to-cart (`cart.go`)

The cart keys on the **shared `sid`** (§7.3), not a shop-private `cart_id` cookie, so an anonymous
buyer has one identity across shop and booking. `api_auth.SessionID` mints the cookie on first use.

```go
func (s *Service) AddToCart(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := api_auth.SessionID(w, r) // shared guest/session id (was the shop's cart_id)
		slug := r.FormValue("slug")
		qty, _ := strconv.Atoi(r.FormValue("qty"))
		if qty <= 0 {
			qty = 1
		}

		p, err := s.Catalog.BySlug(r.Context(), slug)
		if err != nil {
			http.Error(w, "product not found", http.StatusNotFound)
			return
		}

		cart, err := s.Carts.Load(r.Context(), id)
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
		_ = s.Carts.Save(r.Context(), id, cart)

		http.Redirect(w, r, "/shop/cart", http.StatusSeeOther)
	}
}
```

### 4.4 Wiring into `server.go`

Add a `shop` group to `Routes()`. Public routes are open; the `admin` subgroup reuses
`api_auth.AuthMiddleware` (extended with a role check — see §7).

> **Route strings: exactly one space between method and path.** The repo's router splits on the
> first space (`strings.Cut(path, " ")`) and does not trim spaces from the path, so `"GET  /cart"`
> (two spaces, e.g. for column alignment) registers the malformed pattern `/shop/ /cart` and the
> real URL silently 404s. Keep them single-spaced; don't pad for alignment.

```go
shopSvc := shopserver.New(s.context)

mux.Group("shop", func(shop *router.Router) {
	// ---- storefront (public) ----
	shop.HandleFunc("GET /",                 shopSvc.Landing(s.context))
	shop.HandleFunc("GET /products",         shopSvc.ProductList(s.context))
	shop.HandleFunc("GET /products/{slug}",  shopSvc.ProductDetail(s.context))
	shop.HandleFunc("GET /categories/{slug}", shopSvc.CategoryList(s.context))

	// ---- cart ----
	shop.HandleFunc("GET /cart",         shopSvc.ViewCart(s.context))
	shop.HandleFunc("POST /cart/add",     shopSvc.AddToCart(s.context))
	shop.HandleFunc("POST /cart/update",  shopSvc.UpdateCart(s.context))
	shop.HandleFunc("POST /cart/remove",  shopSvc.RemoveFromCart(s.context))

	// ---- checkout ----
	shop.HandleFunc("GET /checkout",        shopSvc.Checkout(s.context))
	shop.HandleFunc("POST /checkout",        shopSvc.SubmitCheckout(s.context))
	shop.HandleFunc("GET /orders/{number}", shopSvc.OrderConfirmation(s.context))

	// ---- CMS (protected) ----
	shop.Middleware(api_auth.AdminMiddleware(s.context)).Group("admin", func(admin *router.Router) {
		admin.HandleFunc("GET /",                    shopSvc.AdminDashboard(s.context))
		admin.HandleFunc("GET /products",            shopSvc.AdminProducts(s.context))
		admin.HandleFunc("GET /products/new",        shopSvc.AdminProductForm(s.context))
		admin.HandleFunc("POST /products",            shopSvc.AdminProductCreate(s.context))
		admin.HandleFunc("GET /products/{id}/edit",  shopSvc.AdminProductForm(s.context))
		admin.HandleFunc("POST /products/{id}",       shopSvc.AdminProductUpdate(s.context))
		admin.HandleFunc("POST /products/{id}/delete", shopSvc.AdminProductDelete(s.context))
		admin.HandleFunc("POST /products/{id}/images", shopSvc.AdminImageUpload(s.context))

		admin.HandleFunc("GET /categories",          shopSvc.AdminCategories(s.context))
		admin.HandleFunc("POST /categories",          shopSvc.AdminCategoryCreate(s.context))

		admin.HandleFunc("GET /orders",              shopSvc.AdminOrders(s.context))
		admin.HandleFunc("GET /orders/{number}",     shopSvc.AdminOrderDetail(s.context))
		admin.HandleFunc("POST /orders/{number}/status", shopSvc.AdminOrderStatus(s.context))
	})
}, api_auth.CSRF()) // group-level CSRF: no-op on safe methods, guards every POST (§7.4)
```

Add a `"/shop"` nav entry in `internal/web/components/header.templ` (`@NavItem("/shop", false, "Shop")`).

---

## 5. Storefront views (`internal/web/pages/shop`, package `shopviews`)

These are `templ` components taking typed data. They inherit the `web.Base` shell (header/footer,
dark mode, Tailwind CSS at `/static/css/main.css`, Alpine.js from CDN) so styling is consistent
with the rest of the site. Tailwind Plus "Ecommerce" component markup drops straight into these
files — the classes below are already in that idiom.

Product grid + card:

```templ
package shopviews

import (
	sm "github.com/erancihan/clair/internal/shop/models"
	"github.com/erancihan/clair/internal/shop"
)

templ ProductList(products []sm.ShopProduct) {
	<div class="mt-16 sm:px-8">
		<div class="mx-auto max-w-7xl lg:px-8">
			<div class="relative px-4 sm:px-8 lg:px-12">
				<div class="mx-auto max-w-2xl lg:max-w-5xl">
					<h1 class="text-4xl font-bold tracking-tight text-zinc-800 dark:text-zinc-100 sm:text-5xl">
						Shop
					</h1>
					<div class="mt-10 grid grid-cols-1 gap-x-6 gap-y-10 sm:grid-cols-2 lg:grid-cols-3">
						for _, p := range products {
							@ProductCard(p)
						}
					</div>
				</div>
			</div>
		</div>
	</div>
}

templ ProductCard(p sm.ShopProduct) {
	<a href={ templ.SafeURL("/shop/products/" + p.Slug) } class="group block">
		<div class="aspect-square w-full overflow-hidden rounded-2xl bg-zinc-100 dark:bg-zinc-800">
			if len(p.Images) > 0 {
				<img src={ p.Images[0].URL } alt={ p.Images[0].Alt }
					class="h-full w-full object-cover transition group-hover:scale-105"/>
			}
		</div>
		<h3 class="mt-4 text-sm font-medium text-zinc-800 dark:text-zinc-100">{ p.Title }</h3>
		<p class="mt-1 text-sm text-zinc-600 dark:text-zinc-400">
			{ shop.FormatCents(p.PriceCents, p.Currency) }
		</p>
	</a>
}
```

Product detail with an add-to-cart form (plain POST form — no JS required, progressive
enhancement via Alpine optional):

```templ
templ ProductDetail(p *sm.ShopProduct) {
	<div class="mx-auto max-w-2xl px-4 py-16 sm:px-6 lg:max-w-5xl lg:px-8">
		<div class="grid grid-cols-1 gap-x-8 gap-y-10 lg:grid-cols-2">
			<div class="aspect-square overflow-hidden rounded-3xl bg-zinc-100 dark:bg-zinc-800">
				if len(p.Images) > 0 {
					<img src={ p.Images[0].URL } alt={ p.Images[0].Alt } class="h-full w-full object-cover"/>
				}
			</div>
			<div>
				<h1 class="text-3xl font-bold tracking-tight text-zinc-800 dark:text-zinc-100">{ p.Title }</h1>
				<p class="mt-4 text-2xl text-zinc-800 dark:text-zinc-100">
					{ shop.FormatCents(p.PriceCents, p.Currency) }
				</p>
				<div class="mt-6 text-base text-zinc-600 dark:text-zinc-400">{ p.Description }</div>

				<form action="/shop/cart/add" method="POST" class="mt-8 flex items-center gap-4">
					<input type="hidden" name="slug" value={ p.Slug }/>
					<input type="number" name="qty" value="1" min="1"
						class="w-20 rounded-md border border-zinc-300 px-3 py-2 dark:border-zinc-700 dark:bg-zinc-800"/>
					<button type="submit"
						class="rounded-md bg-indigo-600 px-6 py-2.5 text-sm font-semibold text-white hover:bg-indigo-500">
						Add to cart
					</button>
				</form>
			</div>
		</div>
	</div>
}
```

Cart, checkout, and order-confirmation templates follow the same structure (a table of
`cart.Items`, `shop.FormatCents(cart.SubtotalCents(), cart.Currency)` for totals, and a shipping
form that POSTs to `/shop/checkout`).

After adding these files, regenerate: `go tool templ generate` (already wired via `//go:generate`
in `internal/web/embed.go` and `make assets`).

---

## 6. Admin CMS (`/shop/admin`)

Server-rendered CRUD, no SPA. Product create/update accept `multipart/form-data` (so image upload
shares the form). Example create handler + form:

```go
func (s *Service) AdminProductCreate(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		price, _ := strconv.ParseFloat(r.FormValue("price"), 64)
		p := &sm.ShopProduct{
			Title:       r.FormValue("title"),
			Slug:        shop.Slugify(r.FormValue("title")),
			Description: r.FormValue("description"),
			PriceCents:  int64(price * 100), // form shows dollars, DB stores cents
			Currency:    "USD",
			Status:      sm.ShopProductStatus(r.FormValue("status")),
			SKU:         r.FormValue("sku"),
		}
		if err := s.Catalog.Create(r.Context(), p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/shop/admin/products", http.StatusSeeOther)
	}
}
```

```templ
templ ProductForm(p *sm.ShopProduct, action string) {
	<form action={ templ.SafeURL(action) } method="POST" enctype="multipart/form-data"
		class="mx-auto max-w-2xl space-y-6 px-4 py-10">
		<div>
			<label class="block text-sm font-medium">Title</label>
			<input name="title" value={ p.Title } required
				class="mt-1 block w-full rounded-md border px-3 py-2"/>
		</div>
		<div>
			<label class="block text-sm font-medium">Price (USD)</label>
			<input name="price" type="number" step="0.01" value={ fmt.Sprintf("%.2f", float64(p.PriceCents)/100) }
				class="mt-1 block w-full rounded-md border px-3 py-2"/>
		</div>
		<div>
			<label class="block text-sm font-medium">Status</label>
			<select name="status" class="mt-1 block w-full rounded-md border px-3 py-2">
				<option value="draft"     selected?={ p.Status == sm.ShopProductDraft }>Draft</option>
				<option value="published" selected?={ p.Status == sm.ShopProductPublished }>Published</option>
			</select>
		</div>
		<textarea name="description" rows="6" class="block w-full rounded-md border px-3 py-2">{ p.Description }</textarea>
		<button type="submit" class="rounded-md bg-indigo-600 px-6 py-2.5 text-sm font-semibold text-white">Save</button>
	</form>
}
```

Admin order management is a list view + detail with a status `<select>` POSTing to
`/shop/admin/orders/{number}/status`.

---

## 7. Auth, identity & app-wide security (shop-owned shared infrastructure)

Per the booking-reconciliation brief (§2.1), the shop **owns** the shared auth/identity/security
primitives and the booking domain reuses them. All of the below lives in
`internal/server/authentication` (imported as `api_auth` in `server.go`) — **not** a shop-only
package — so `/appointments`, `/events`, and `/admin/*` can consume it without importing `shop`.

_Snippets below elide `import` blocks for brevity; each uses only stdlib + existing repo packages
(the new files need: `identity.go` → `context`; `middleware.go` also `strings`, `net/url`;
`session.go` → `net/http`, `encoding/base64`, `crypto/rand`, `fmt`; `csrf.go` → `crypto/subtle`,
`net/http`; `constant.go` also `os`)._

### 7.1 Roles & `AdminMiddleware`

`models.User` gains a `Role` (string, so there's headroom for `vendor`/`staff` later without a
schema change). `AdminMiddleware` layers a role gate on the session check.

```go
// internal/database/models/users.go — User is the ONE shared model (bare `users` table).
type User struct {
	gorm.Model
	ID       uint   `json:"id" gorm:"primaryKey"`
	Username string `json:"username"`
	Email    string `json:"email" gorm:"uniqueIndex"`
	Password string `json:"password"`
	Role     string `json:"role" gorm:"index;default:customer"` // see RoleCustomer/RoleAdmin
}
```

```go
// internal/server/authentication/roles.go
const (
	RoleCustomer = "customer"
	RoleAdmin    = "admin"
)

// AdminMiddleware wraps AuthMiddleware (so identity is already in context) and gates on role.
// Booking's /admin/* routes reuse this verbatim.
func AdminMiddleware(ctx server_context.BackEndContext) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return AuthMiddleware(ctx)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := CurrentUser(r.Context()) // no second DB query — AuthMiddleware injected it
			if !ok || id.Role != RoleAdmin {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}
```

### 7.2 Request-context identity — `AuthMiddleware` injection + `CurrentUser`

Today `AuthMiddleware` validates the session but injects nothing, so no handler can cheaply learn
who the caller is. We fix that: `AuthMiddleware` stores an `Identity{UserID, Role}` in the request
context, and `CurrentUser(ctx)` reads it. This is **load-bearing for booking** — it has no other
source of "who is this."

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

```go
// middleware.go — inject identity on success; content-negotiate the failure (decided, §12).
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

// unauthorized: browsers → redirect to /login (with return path); API/JSON → 401.
func unauthorized(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") ||
		r.Header.Get("Content-Type") == "application/json" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.Path), http.StatusFound)
}
```

> **`next` must be honored by the login flow.** Today `LoginPage` ignores the request and `AuthLogin`
> hard-redirects to `/dashboard` (`login.go`). For the redirect above to actually return the user to
> the deep-linked admin URL, `LoginPage` must render `next` into a hidden field and `AuthLogin` must
> redirect to it **after validating it is a same-origin, leading-slash path** (reject absolute URLs /
> `//host` to avoid an open redirect), falling back to `/dashboard`. Do this in Phase 0 or drop the
> `next` param.

### 7.3 Shared session / guest identity (`sid`) — used by cart AND booking

The shop's cart key and booking's guest `OwnerRef` must be the **same** anonymous id, so we
generalize the old `cart_id` cookie into one `sid` cookie scoped to `/` (every domain sees it).
`OwnerRef` is the single answer to "who owns this cart / hold / order."

```go
// internal/server/authentication/session.go
const SessionCookie = "sid" // shared guest/session id (was the shop's cart_id)

// SessionID returns a stable per-browser id, minting an HttpOnly cookie on first use.
func SessionID(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		return c.Value
	}
	id := SecureToken() // NOT utils.GenerateGameID (~47 bits) — see note below
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: id, Path: "/", // "/" not "/shop" — booking reads it too
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: 90 * 24 * 3600, Secure: SecureCookies(),
	})
	return id
}

// OwnerRef owns carts, holds, and orders: "user:<id>" when authenticated, else "guest:<sid>".
// This is the canonical format; booking adopts it (its §6.3 "sess:abc" examples are illustrative
// and superseded by this "user:"/"guest:" prefixing).
func OwnerRef(w http.ResponseWriter, r *http.Request) string {
	if id, ok := CurrentUser(r.Context()); ok {
		return fmt.Sprintf("user:%d", id.UserID)
	}
	return "guest:" + SessionID(w, r)
}
```

The shop cart (§4.3) now keys on `api_auth.SessionID(w, r)` instead of its own `cart_id` cookie.
(Nice-to-have: on login, migrate the `guest:<sid>` cart to the `user:<id>` owner.)

> **`SecureToken()`** is a small helper — `base64.RawURLEncoding.EncodeToString(b)` over 32
> `crypto/rand` bytes (~256 bits). The `sid` is a long-lived (90-day) cross-domain identity and the
> CSRF value is a secret, so **do not** reuse `utils.GenerateGameID` (an ~8-char, ~47-bit game id)
> for either — it's built for short-lived game instances.

### 7.4 App-wide CSRF (one strategy, both domains)

A synchronizer token bound to the session, verified by one shared middleware on every unsafe method
across shop **and** booking mutating routes (add-to-cart, checkout, order-status, and booking's
`/hold` · `/checkout` · `/cancel`). The **payment webhook is exempt** — it's authenticated by the
provider's signature, not a browser session.

```go
// internal/server/authentication/csrf.go
func CSRFToken(r *http.Request) (string, error) { // read-or-create; render into a hidden field
	sess, _ := store.Get(r, SESSION_NAME)
	if t, ok := sess.Values["csrf"].(string); ok && t != "" {
		return t, nil
	}
	sess.Values["csrf"] = SecureToken() // 256-bit, not GenerateGameID (see §7.3 note)
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

Wire it per group (the custom router's `Group` takes trailing middleware) — on `/shop` and booking's
`/appointments` · `/events` · `/admin`, but **not** `/webhooks`:

```go
mux.Group("shop", func(shop *router.Router) { /* … */ }, api_auth.CSRF())
```

Every state-changing templ form carries `<input type="hidden" name="csrf_token" value={ token }>`,
where `token` comes from `api_auth.CSRFToken(r)` (thread it through the view data). AJAX callers send
the `X-CSRF-Token` header instead.

### 7.5 Session hardening

Replace the hard-coded signing key (`constant.go`) with an env secret that **fails closed** in
production, and make cookies `Secure` behind TLS. This affects every authed/guest flow in both
domains, so it is a shared prerequisite, not a shop nicety.

```go
// internal/server/authentication/constant.go
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

`AuthLogin` (`login.go`) should set `Secure: api_auth.SecureCookies()` on the session cookie instead
of the hard-coded `false`.

---

## 8. Image uploads

Simplest path that matches the current static-serving setup: save uploads to the `public/` dir
(served by the existing `GET /public/` route) and store the relative URL in `ShopProductImage.URL`.

```go
func (s *Service) AdminImageUpload(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		if err := r.ParseMultipartForm(10 << 20); err != nil { // 10MB
			http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
			return
		}
		file, hdr, err := r.FormFile("image")
		if err != nil { http.Error(w, "no file", http.StatusBadRequest); return }
		defer file.Close()

		// content-type allow-list (test §10.6 asserts non-image rejection)
		if ct := hdr.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
			http.Error(w, "not an image", http.StatusBadRequest)
			return
		}

		dir := filepath.Join(web.Public(), "products")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		name := fmt.Sprintf("products/%d-%s", id, filepath.Base(hdr.Filename)) // Base() strips ../
		dst, err := os.Create(filepath.Join(web.Public(), name))
		if err != nil { http.Error(w, err.Error(), http.StatusInternalServerError); return }
		defer dst.Close()
		if _, err := io.Copy(dst, file); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := s.Catalog.AddImage(r.Context(), uint(id), "/public/"+name, r.FormValue("alt")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/shop/admin/products/%d/edit", id), http.StatusSeeOther)
	}
}
```

(Note: the `Content-Type` header is client-supplied, so it's a cheap first gate, not a guarantee —
for real assurance sniff the first 512 bytes with `http.DetectContentType`.)

Upgrade path: the project already depends on `aws-sdk-go`, so an S3-backed `ImageStore` interface
is a clean later swap. Add `internal/web/public/products/` to `.gitignore` for local uploads.

---

## 9. Asset pipeline / Tailwind v4 notes

- CSS entry is `internal/web/css/main.css` = `@import "tailwindcss";` (v4). v4 auto-detects source
  files, which is why current pages get their classes even though `tailwind.config.js`'s `content`
  array only lists `login.templ`. To make it explicit and robust for the new `shop/` views, add a
  source directive to `main.css`:

  ```css
  @import "tailwindcss";
  @source "../";           /* scans all *.templ under internal/web */
  ```

- Build stays the same: `make assets` runs `npm run css` (gulp → postcss → tailwind → cssnano) then
  `go generate ./...` (templ). `make dev` uses `air`, which already watches `.templ`.
- No new JS build step. Add-to-cart works as plain forms; use the already-loaded Alpine.js only for
  nice-to-haves (quantity steppers, a cart-count badge).

---

## 10. Testing

Testing is **primarily endpoint (HTTP integration) testing**: each test drives the real
`Routes()` handler through `net/http/httptest` with an in-memory SQLite DB and a `nil` Valkey
client (so the cart uses its DB fallback store). A handful of pure-domain unit tests back the
money/cart math. Everything lives under `test/` and runs with `go test ./test/... -v`.

> **SQLite here, Postgres in production.** Production shares one Postgres DB with the booking domain
> (§2.1), but the shop uses only portable GORM tags and the `Shop*` type prefix derives `shop_*`
> table names identically on any driver, so in-memory SQLite is a valid, fast backend for shop-only
> unit/endpoint tests. Reserve a Postgres backend (testcontainers/dockertest, as the booking suite
> uses) for cross-domain integration tests that exercise both `shop_*` and `booking_*` in one DB.

### 10.1 Test harness

```go
package test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/erancihan/clair/internal/database/models" // shared: User
	sm "github.com/erancihan/clair/internal/shop/models"  // shop: Shop*
	"github.com/erancihan/clair/internal/server"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// newTestServer spins up the real Routes() against an isolated in-memory DB.
// nil Valkey => the cart uses the DB fallback store, so no Valkey is needed in CI.
func newTestServer(t *testing.T) (*httptest.Server, *gorm.DB) {
	t.Helper()
	// unique DSN per test => full isolation even when tests run in parallel
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.AutoMigrate(
		&models.User{}, &sm.ShopCategory{}, &sm.ShopProduct{},
		&sm.ShopProductImage{}, &sm.ShopOrder{}, &sm.ShopOrderItem{}, &sm.ShopCart{},
	)
	handler := server.NewBackEnd(context.Background(), zap.NewNop(), nil, db).Routes()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts, db
}

// newClient keeps a cookie jar (session + sid persist across requests) and does
// NOT auto-follow redirects, so tests can assert 303 + Location.
func newClient(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func seedAdmin(t *testing.T, db *gorm.DB, email, pw string) {
	hash, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	db.Create(&models.User{Username: "admin", Email: email, Password: string(hash), Role: "admin"})
}

// loginAs authenticates c against the JSON login endpoint; the session cookie lands in c's jar.
func loginAs(t *testing.T, ts *httptest.Server, c *http.Client, email, pw string) {
	body := strings.NewReader(`{"email":"` + email + `","password":"` + pw + `"}`)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("login failed: err=%v status=%d", err, resp.StatusCode)
	}
}
```

Table-driven endpoint test (the pattern every table below compiles down to):

```go
func TestProductDetail(t *testing.T) {
	ts, db := newTestServer(t)
	db.Create(&sm.ShopProduct{Slug: "blue-shirt", Title: "Blue Shirt", Status: sm.ShopProductPublished, PriceCents: 1299, Currency: "USD"})
	db.Create(&sm.ShopProduct{Slug: "secret", Title: "Secret", Status: sm.ShopProductDraft, PriceCents: 500, Currency: "USD"})

	cases := []struct {
		name, path string
		wantStatus int
		wantBody   string // substring; "" to skip
	}{
		{"published renders price", "/shop/products/blue-shirt", 200, "$12.99"},
		{"unknown slug 404", "/shop/products/ghost", 404, ""},
		{"draft hidden 404", "/shop/products/secret", 404, ""},
		{"wrong method 405", "/shop/products/blue-shirt", 405, ""}, // via POST in the real test
	}
	c := newClient(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := c.Get(ts.URL + tc.path)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("got %d want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantBody != "" {
				b, _ := io.ReadAll(resp.Body)
				if !strings.Contains(string(b), tc.wantBody) {
					t.Fatalf("body missing %q", tc.wantBody)
				}
			}
		})
	}
}
```

**Conventions used in the tables below**
- *Type* column tags coverage: Happy, Validation, AuthN, AuthZ, Not found, Method, Boundary, Edge, State, Security.
- Successful POSTs use Post/Redirect/Get → `303 See Other` with a `Location` header.
- 🚩 marks a design decision the test *pins down* — confirm the intended behavior before writing the assertion.

### 10.2 Unit tests (pure domain, no HTTP)

| # | Target | Scenario | Expected |
|---|---|---|---|
| 1 | `Cart.Upsert` | add new / merge existing qty / qty≤0 removes line | items & quantities correct |
| 2 | `Cart.SubtotalCents` / `Count` | mixed items & quantities | Σ(unit×qty); total item count |
| 3 | `FormatCents` | 1299/USD, 0, negative, unmapped currency (`JPY`) | `$12.99`, `$0.00`, `-$…`, `JPY …` (mapped syms like `€` resolve; unmapped fall back to `CODE `) |
| 4 | `PlaceOrder` | snapshot lines, subtotal math, out-of-stock rollback, empty-cart error | order+items or `ErrOutOfStock`/`ErrEmptyCart`; stock unchanged on failure |
| 5 | `Slugify` | spaces/case/punctuation, collision suffixing | stable, unique slugs |

### 10.3 Storefront (public reads)

#### `GET /shop/`

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result / side effects |
|---|---|---|---|---|---|---|
| 1 | Happy | Landing renders | some published products + categories | `GET /shop/` | 200 | base shell, featured products & category links |
| 2 | Edge | Empty catalog | no products | `GET /shop/` | 200 | empty-state, no product cards |
| 3 | Edge | Only drafts exist | all products draft | `GET /shop/` | 200 | no products shown (published-only) |
| 4 | Method | Wrong verb | — | `POST /shop/` | 405 | Method Not Allowed |

#### `GET /shop/products`

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result / side effects |
|---|---|---|---|---|---|---|
| 1 | Happy | Default first page | >12 published | `GET /shop/products` | 200 | 12 items, newest first |
| 2 | Happy | Pagination | >12 published | `?page=2` | 200 | second page items |
| 3 | Happy | Search match | product "Blue Shirt" | `?search=Blue` | 200 | only matching products |
| 4 | Happy | Category filter | category "hats" populated | `?category=hats` | 200 | only that category's published |
| 5 | Edge | Search no match | — | `?search=zzz` | 200 | empty result grid |
| 6 | Edge | Drafts excluded | mixed draft/published | `GET /shop/products` | 200 | drafts absent |
| 7 | Boundary | Page beyond range | 5 products | `?page=99` | 200 | empty grid, no error |
| 8 | Boundary | page ≤0 / non-numeric | — | `?page=0` / `?page=abc` | 200 | defaults to page 1 |
| 9 | Edge | Unknown category filter | — | `?category=nope` | 200 | empty list (§12) — consistent with search, not 404 |
| 10 | Security | Search XSS | — | `?search=<script>` | 200 | value escaped (templ auto-escape) |
| 11 | Method | Wrong verb | — | `POST /shop/products` | 405 | Method Not Allowed |

#### `GET /shop/products/{slug}`

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result / side effects |
|---|---|---|---|---|---|---|
| 1 | Happy | Published detail | published slug `x` | `GET …/x` | 200 | title, formatted price, add-to-cart form, images |
| 2 | Edge | No images | product without images | `GET …/x` | 200 | renders without `<img>`, no crash |
| 3 | Edge | Price formatting | price 1299 USD | `GET …/x` | 200 | shows `$12.99` |
| 4 | Not found | Unknown slug | — | `GET …/ghost` | 404 | NotFound shell |
| 5 | Not found | Draft product | slug `d` is draft | `GET …/d` | 404 | hidden |
| 6 | Not found | Archived product | slug `a` archived | `GET …/a` | 404 | hidden |
| 7 | Security | Malicious slug | — | `GET …/<script>` / `../` | 404 / escaped | no traversal, escaped |
| 8 | Method | Wrong verb | — | `POST …/x` | 405 | Method Not Allowed |

#### `GET /shop/categories/{slug}`

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result / side effects |
|---|---|---|---|---|---|---|
| 1 | Happy | Category with products | "hats" has published products | `GET …/hats` | 200 | lists that category's published products |
| 2 | Edge | Empty category | category exists, no products | `GET …/hats` | 200 | empty-state |
| 3 | Happy | Pagination | >12 in category | `?page=2` | 200 | page 2 |
| 4 | Not found | Unknown category | — | `GET …/none` | 404 | NotFound shell |
| 5 | Method | Wrong verb | — | `POST …/hats` | 405 | Method Not Allowed |

### 10.4 Cart

#### `GET /shop/cart`

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result / side effects |
|---|---|---|---|---|---|---|
| 1 | Happy | View populated cart | `sid` cookie with items | `GET /shop/cart` | 200 | items, qty, line + subtotal totals |
| 2 | Happy | Empty cart | cookie present, empty | `GET /shop/cart` | 200 | empty-cart message, checkout disabled |
| 3 | Edge | No cookie → new cart | no `sid` | `GET /shop/cart` | 200 | `Set-Cookie: sid`; empty state |
| 4 | Edge | Subtotal correctness | items qty 2 & 3 | `GET /shop/cart` | 200 | subtotal = Σ(unit×qty) |
| 5 | Method | Wrong verb | — | `PUT /shop/cart` | 405 | Method Not Allowed |

#### `POST /shop/cart/add`

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result / side effects |
|---|---|---|---|---|---|---|
| 1 | Happy | Add new item | published `x`, no/empty cart | `slug=x&qty=1` | 303 | `Set-Cookie sid`; redirect `/shop/cart`; line added |
| 2 | Happy | Merge quantity | `x` already in cart (qty 1) | `slug=x&qty=2` | 303 | qty → 3, single line |
| 3 | Validation | Missing slug | — | `qty=1` | 404 | product not found |
| 4 | Not found | Unknown slug | — | `slug=ghost&qty=1` | 404 | product not found |
| 5 | Edge | Draft not addable | `d` is draft | `slug=d&qty=1` | 404 | published-only lookup fails |
| 6 | Boundary | qty missing / 0 / negative | — | `slug=x` / `qty=0` / `qty=-3` | 303 | qty clamped to 1 |
| 7 | Boundary | Non-numeric qty | — | `slug=x&qty=abc` | 303 | Atoi fails → qty 1 |
| 8 | Boundary | Huge qty | — | `slug=x&qty=999999` | 303 | accepted; stock checked at checkout 🚩 |
| 9 | Security | Price tampering | — | `slug=x&price=1` | 303 | price sourced from DB, form price ignored |
| 10 | Security | CSRF | cross-site POST, no token | `slug=x&qty=1` | 403 | rejected by CSRF middleware (§7.4) |
| 11 | Method | Wrong verb | — | `GET /shop/cart/add` | 405 | Method Not Allowed |

#### `POST /shop/cart/update`

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result / side effects |
|---|---|---|---|---|---|---|
| 1 | Happy | Change quantity | item in cart | `product_id=1&qty=5` | 303 | line qty → 5; redirect `/shop/cart` |
| 2 | Edge | qty ≤0 removes line | item in cart | `product_id=1&qty=0` | 303 | line removed |
| 3 | Edge | Product not in cart | — | `product_id=99&qty=2` | 303 | no-op |
| 4 | Edge | No cart cookie | no cookie | `product_id=1&qty=2` | 303 | empty cart, no-op |
| 5 | Validation | Missing product_id | — | `qty=2` | 400 / 303 🚩 | handled, no crash |
| 6 | Boundary | Non-numeric id/qty | — | `product_id=x&qty=y` | 400 / 303 | handled, no crash |
| 7 | Method | Wrong verb | — | `GET /shop/cart/update` | 405 | Method Not Allowed |

#### `POST /shop/cart/remove`

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result / side effects |
|---|---|---|---|---|---|---|
| 1 | Happy | Remove existing | item in cart | `product_id=1` | 303 | line removed; redirect `/shop/cart` |
| 2 | Edge | Remove absent | not in cart | `product_id=99` | 303 | no-op |
| 3 | State | Remove twice | — | `product_id=1` ×2 | 303 | second is no-op |
| 4 | Validation | Missing product_id | — | (empty body) | 400 / 303 🚩 | handled |
| 5 | Method | Wrong verb | — | `GET /shop/cart/remove` | 405 | Method Not Allowed |

### 10.5 Checkout & Orders (public)

#### `GET /shop/checkout`

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result / side effects |
|---|---|---|---|---|---|---|
| 1 | Happy | Show form | non-empty cart cookie | `GET /shop/checkout` | 200 | shipping form + order summary |
| 2 | Edge | Empty cart | empty / absent cart | `GET /shop/checkout` | 303 → `/shop/cart` 🚩 | redirect or empty-state |
| 3 | Method | Wrong verb | — | `PUT /shop/checkout` | 405 | Method Not Allowed |

#### `POST /shop/checkout`

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result / side effects |
|---|---|---|---|---|---|---|
| 1 | Happy | Place order (guest) | non-empty cart, valid fields | `email,name,address,city,postal,country` | 303 | Order+OrderItems (`pending_payment`), stock decremented, cart cleared; redirect `/shop/orders/{number}` |
| 2 | Edge | Snapshot integrity | product edited after order | edit product later | — | OrderItem keeps original title/price |
| 3 | Edge | Empty cart | empty cart | valid fields | 400 🚩 | `ErrEmptyCart`, no order |
| 4 | Edge | Out of stock | item qty > stock | valid fields | 409 / 400 🚩 | `ErrOutOfStock`, no order, stock unchanged (rollback) |
| 5 | Boundary | Stock exactly equal | qty == stock | valid fields | 303 | succeeds, stock → 0 |
| 6 | Validation | Missing required field | cart ok, no email | omit `email` | 400 | re-render with error, no order |
| 7 | Validation | Invalid email | — | `email=notanemail` | 400 | error, no order |
| 8 | State | Double submit | same cart posted twice | POST ×2 | 303, then 400 | cart cleared on success (§3.4); 2nd sees empty cart → `ErrEmptyCart` |
| 9 | Security | Price/total tampering | hidden price/total field | tampered fields | 303 | server recomputes totals from cart/DB |
| 10 | Security | CSRF | cross-site POST, no token | valid fields | 403 | rejected by CSRF middleware (§7.4) |
| 11 | Method | Wrong verb | — | `DELETE /shop/checkout` | 405 | Method Not Allowed |

#### `GET /shop/orders/{number}`

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result / side effects |
|---|---|---|---|---|---|---|
| 1 | Happy | View order | order `N` exists | `GET …/N` | 200 | items, totals, status |
| 2 | Not found | Unknown number | — | `GET …/ZZZ` | 404 | NotFound shell |
| 3 | Security | IDOR — guest | guess another's number, not logged in | `GET …/<other>` | 200 | number is high-entropy/unguessable (§12); no enumeration |
| 3b | Security | IDOR — authed | logged-in user requests an order not their `UserID` | `GET …/<other>` | 404 | scoped by `UserID` (§12) |
| 4 | Method | Wrong verb | — | `POST …/N` | 405 | Method Not Allowed |

### 10.6 Admin CMS (protected)

**Admin access-control matrix** — applies to **every** `/shop/admin/*` route (assert once per route, or via a shared helper):

Failure is content-negotiated (decided, §7.2/§12): browsers get a redirect to `/login`, JSON callers
get a status code.

| # | Scenario | State | Request | Expected status | Expected result |
|---|---|---|---|---|---|
| 1 | Unauthenticated (browser) | no session cookie, `Accept: text/html` | any `/shop/admin/*` | 302 | redirect to `/login?next=…`; handler never runs |
| 2 | Unauthenticated (API) | no session cookie, `Accept: application/json` | any admin route | 401 | Unauthorized; handler never runs |
| 3 | Invalid/expired session | malformed cookie | any admin route | 302 / 401 | per content negotiation |
| 4 | Authenticated non-admin | `role=customer` session | any admin route | 403 | Forbidden (no redirect loop) |
| 5 | Authenticated admin | `role=admin` session | any admin route | 2xx/3xx | proceeds per endpoint |
| 6 | Session user deleted | valid cookie, user row gone | any admin route | 302 / 401 | middleware re-checks user, then negotiates |
| 7 | CSRF missing/bad on mutation | admin session, POST without token | any admin `POST` | 403 | `bad CSRF token` (§7.4) |

Per-endpoint tables below assume an **admin session** and omit the AuthN/AuthZ rows covered above.

#### `GET /shop/admin/` · `GET /shop/admin/products` · `GET /shop/admin/products/new`

| # | Type | Endpoint | Scenario | Request | Expected status | Expected result |
|---|---|---|---|---|---|---|
| 1 | Happy | `/shop/admin/` | Dashboard | `GET` | 200 | counts + links |
| 2 | Happy | `/shop/admin/products` | List all statuses | `GET` | 200 | draft+published+archived, edit/delete links |
| 3 | Happy | `/shop/admin/products` | Empty | `GET` | 200 | empty-state |
| 4 | Happy | `/shop/admin/products/new` | Blank form | `GET` | 200 | empty form, `action=/shop/admin/products` |
| 5 | Method | all three | Wrong verb | `POST`/`PUT` | 405 | Method Not Allowed |

#### `POST /shop/admin/products` (create)

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result / side effects |
|---|---|---|---|---|---|---|
| 1 | Happy | Create published | admin, valid multipart | `title,price=12.99,status=published,sku,description` | 303 | Product row, slug from title, `PriceCents=1299`; redirect list |
| 2 | Happy | Create draft | — | `status=draft` | 303 | created as draft |
| 3 | Edge | Dollars → cents | — | `price=12.99` | 303 | `PriceCents=1299` |
| 4 | Boundary | Free item | — | `price=0` | 303 | `PriceCents=0` allowed |
| 5 | Validation | Missing title | — | omit `title` | 400 | error, no row |
| 6 | Validation | Invalid price | — | `price=abc` / `price=-5` | 400 | error, no row |
| 7 | Edge | Slug collision | title maps to existing slug | valid | 303 | auto-suffixed slug (`-2`, `-3`, …) per §12; created |
| 8 | Security | XSS in fields | — | `title=<script>` | 303 | stored raw, escaped on render |
| 9 | Method | Wrong verb | — | `PUT /shop/admin/products` | 405 | Method Not Allowed |

#### `GET /shop/admin/products/{id}/edit`

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result |
|---|---|---|---|---|---|---|
| 1 | Happy | Prefilled form | product `id=1` | `GET …/1/edit` | 200 | form with values + existing images |
| 2 | Not found | Unknown id | — | `GET …/999/edit` | 404 | NotFound |
| 3 | Boundary | Non-numeric id | — | `GET …/abc/edit` | 404 / 400 | handled |
| 4 | Method | Wrong verb | — | `POST …/1/edit` | 405 | Method Not Allowed |

#### `POST /shop/admin/products/{id}` (update)

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result / side effects |
|---|---|---|---|---|---|---|
| 1 | Happy | Update fields | product `id=1` | `title,price,status` | 303 | row updated; redirect |
| 2 | State | Publish draft | draft product | `status=published` | 303 | now visible on storefront |
| 3 | State | Archive | published product | `status=archived` | 303 | hidden from storefront |
| 4 | Not found | Unknown id | — | `POST …/999` | 404 | NotFound |
| 5 | Validation | Invalid price | — | `price=-1` | 400 | no change |
| 6 | Boundary | Non-numeric id | — | `POST …/abc` | 404 / 400 | handled |
| 7 | Method | Wrong verb | — | `DELETE …/1` | 405 | Method Not Allowed |

#### `POST /shop/admin/products/{id}/delete`

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result / side effects |
|---|---|---|---|---|---|---|
| 1 | Happy | Delete | product `id=1` | `POST …/1/delete` | 303 | soft-deleted (`DeletedAt`); gone from lists |
| 2 | Not found | Unknown id | — | `POST …/999/delete` | 404 | NotFound |
| 3 | State | Delete twice | — | `POST …/1/delete` ×2 | 303 → 404 | second no-op/404 |
| 4 | Edge | Referenced by orders | product has OrderItems | `POST …/1/delete` | 303 | OrderItems keep snapshot (no cascade) |
| 5 | Method | Wrong verb | — | `GET …/1/delete` | 405 | Method Not Allowed |

#### `POST /shop/admin/products/{id}/images`

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result / side effects |
|---|---|---|---|---|---|---|
| 1 | Happy | Upload image | product `id=1`, valid PNG | multipart `image` | 303 | file under `public/products/`, ProductImage row, `/public/…` URL; redirect edit |
| 2 | Validation | No file | — | no `image` field | 400 | error |
| 3 | Validation | Non-image type | upload `.exe` | multipart | 400 | rejected by `image/*` allow-list + sniff (§8, §12) |
| 4 | Boundary | Too large (>10MB) | — | big file | 400 / 413 | rejected |
| 5 | Not found | Unknown product id | — | `POST …/999/images` | 404 | NotFound |
| 6 | Security | Filename traversal | `filename=../../x` | multipart | 303 | sanitized via `filepath.Base` |
| 7 | Method | Wrong verb | — | `GET …/1/images` | 405 | Method Not Allowed |

#### `GET /shop/admin/categories` · `POST /shop/admin/categories`

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result / side effects |
|---|---|---|---|---|---|---|
| 1 | Happy | List + form | admin | `GET /shop/admin/categories` | 200 | categories list + inline create form |
| 2 | Happy | Create | admin | `POST name,description` | 303 | Category row, slug from name; redirect |
| 3 | Edge | Parent category | parent exists | `POST name,parent_id=1` | 303 | nested category |
| 4 | Validation | Missing name | — | `POST` (no name) | 400 | error |
| 5 | Edge | Duplicate slug | name collides | `POST name` | 303 | auto-suffixed slug per §12; created |
| 6 | Validation | Invalid parent_id | parent 999 | `POST parent_id=999` | 400 🚩 | error / ignored |
| 7 | Security | XSS name | — | `POST name=<x>` | 303 | escaped on render |
| 8 | Method | Wrong verb | — | `PUT /shop/admin/categories` | 405 | Method Not Allowed |

#### `GET /shop/admin/orders` · `GET /shop/admin/orders/{number}`

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result |
|---|---|---|---|---|---|---|
| 1 | Happy | List orders | orders exist | `GET /shop/admin/orders` | 200 | number, status, total, email |
| 2 | Happy | Empty | none | `GET /shop/admin/orders` | 200 | empty-state |
| 3 | Edge | Filter by status | orders in mixed states | `?status=paid` | 200 🚩 | filtered (if implemented) |
| 4 | Happy | Order detail | order `N` | `GET …/N` | 200 | item snapshots, totals, shipping, status control |
| 5 | Not found | Unknown number | — | `GET …/ZZZ` | 404 | NotFound |
| 6 | Method | Wrong verb | — | `PUT …/N` | 405 | Method Not Allowed |

#### `POST /shop/admin/orders/{number}/status`

| # | Type | Scenario | Preconditions / state | Request | Expected status | Expected result / side effects |
|---|---|---|---|---|---|---|
| 1 | Happy | Mark paid | order `N` = pending | `status=paid` | 303 | status updated; redirect detail |
| 2 | Happy | Fulfil | order paid | `status=fulfilled` | 303 | updated |
| 3 | Happy | Cancel | order pending/paid | `status=cancelled` | 303 | updated (consider restock 🚩) |
| 4 | State | Same status | pending → pending | `status=pending_payment` | 303 | no-op OK |
| 5 | State | Illegal transition | paid → pending | `status=pending_payment` | 400 | rejected by the state-machine guard (§12); status unchanged |
| 6 | Validation | Invalid status value | — | `status=banana` | 400 | rejected, unchanged |
| 7 | Not found | Unknown number | — | `POST …/ZZZ/status` | 404 | NotFound |
| 8 | Security | CSRF | cross-site POST, no token | `status=paid` | 403 | rejected by CSRF middleware (§7.4) |
| 9 | Method | Wrong verb | — | `GET …/N/status` | 405 | Method Not Allowed |

### 10.7 Cross-cutting & routing

| # | Scenario | Preconditions / state | Request | Expected status | Expected result / side effects |
|---|---|---|---|---|---|
| 1 | Unknown path | — | `GET /shop/nonexistent` | 404 | NotFound shell |
| 2 | Method-not-allowed | registered path, wrong verb | `DELETE /shop/products` | 405 | Method Not Allowed |
| 3 | Static assets still served | — | `GET /static/css/main.css` | 200 | CSS from embed FS |
| 4 | Public assets still served | — | `GET /public/favicon.svg` | 200 | served from disk |
| 5 | Nav link present | — | `GET /shop/` | 200 | header includes `/shop` link |
| 6 | Session cookie attributes | first cart interaction | `POST /shop/cart/add` | 303 | `Set-Cookie sid` HttpOnly, `Path=/`, SameSite=Lax (shared with booking, §7.3) |
| 7 | Base shell integrity | — | `GET /shop/` | 200 | dark-mode script, Alpine CDN, `main.css` link |
| 8 | Admin auth uniformity | unauth request to each admin route | table over all `/shop/admin/*` | 302 / 401 | browser → `/login`, JSON → 401 (§7.2); every route blocked |
| 9 | CSRF token presence | state-changing forms | `GET` cart/checkout/admin forms | 200 | hidden `csrf_token` field present (§7.4) |
| 10 | Trailing slash | — | `GET /shop` | 301 | → `/shop/` (canonical group root, §12) |
| 11 | Security headers | — | `GET` any page | 200 🚩 | consider CSP / `X-Content-Type-Options` |

### 10.8 Coverage checklist before merge

- [ ] Every endpoint in §4.4 has a happy-path row **and** its `405` wrong-verb row.
- [ ] Every `/shop/admin/*` route asserts the access-control matrix (browser 302 / JSON 401 unauth, 403 non-admin).
- [ ] Cart lifecycle: add → merge → update → remove → cookie creation.
- [ ] Checkout: success (stock ↓, cart cleared), empty-cart, out-of-stock rollback, validation.
- [ ] All 🚩 design decisions resolved and the assertion updated to the chosen behavior.

---

## 11. Phased milestones

Because the shop ships first and owns the shared infrastructure (§2.1), **Phase 0 comes before any
shop UI** — booking picks it up at its own Phase 0.

0. **Shared infrastructure (shop-owned, booking-consumed).** Complete `newPostgresConn` + fix the
   driver-switch bug + `ifPostgres` guard + merged `AutoMigrate` scaffold (§2.3); then `User.Role` +
   `AdminMiddleware` + request-context identity `CurrentUser` + the shared `sid`/`OwnerRef` session
   helper (§7.1–7.3); then app-wide CSRF + session hardening (§7.4–7.5). **Exit:** app stands up on
   both SQLite (dev/tests) and Postgres; `CurrentUser`/`OwnerRef` usable by any handler; CSRF blocks
   a tokenless POST.
1. **Data + domain** — `sm.*` models on the merged `AutoMigrate`, `Catalog`, `Money`, `Slugify`, unit
   tests. No UI.
2. **Storefront read path** — `/shop`, `/shop/products`, `/shop/products/{slug}`, category pages;
   templ views; nav entry. Seed a few products via a tiny CLI/seed to view it.
3. **Cart** — `CartStore` (Valkey + DB fallback), keyed on the shared `sid`, add/update/remove, cart
   page, count badge.
4. **Checkout (mock)** — shipping form, `PlaceOrder`, cart-clear on success, confirmation page,
   `pending_payment` orders.
5. **Admin CMS** — product CRUD, image upload, category CRUD, order list/detail/status (reusing the
   Phase-0 `AdminMiddleware`).
6. **Polish** — pagination, search/filter, empty states, 404s, tests, Tailwind `@source`.

Later / additive: real `PaymentProvider` (Stripe), S3 image store, multi-vendor (`ShopVendor` model +
per-vendor scoping and vendor admin), discounts/coupons, `shop_inventories` stock reservations.

---

## 12. Decisions (locked)

**Cross-domain (reconciled with booking):** shop and booking **share one PostgreSQL database**; shop
tables are prefixed `shop_*` (types `Shop*`, package `internal/shop/models`) disjoint from
`booking_*`; the shop **owns** the shared auth/identity/session/CSRF/DB infrastructure (§2.1, §7,
§2.3) and booking reuses it — one `User`, one `Role`, one `AdminMiddleware`, one `CurrentUser`/`sid`.

**Shop-only (locked per the reconciliation brief — each was a 🚩 in §10):**

| Question | Decision |
|---|---|
| Guest vs login-required checkout | **Guest allowed** (nullable `ShopOrder.UserID`; owner via `OwnerRef`). |
| Launch currency | **Single-currency USD**; `Currency`/`PriceCents` already per-row for later. |
| CMS auth failure mode | **Content-negotiated**: browsers → 302 `/login?next=…`; JSON → 401; logged-in non-admin → 403 (§7.2). |
| Order-number IDOR (`GET /shop/orders/{number}`) | **Unguessable high-entropy `Number`** (`SecureToken()`, §7.3 — not the ~47-bit game id, since for guests it's the only guard); for authenticated buyers also scope by `UserID`. No enumeration. (A short human-friendly display code, if wanted, is a separate non-security field.) |
| Slug collision (product/category) | **Auto-suffix** `-2`, `-3`, … in `Slugify` on conflict; slug stays `uniqueIndex`. |
| Illegal order-status transition | **State-machine guard**: an allowed-transitions map; anything else → 400, status unchanged. |
| Image upload validation | **≤10MB + `image/*` allow-list** (`hdr.Content-Type`, plus `http.DetectContentType` sniff), §8. |
| Unknown-category filter | **Empty list (200)**, consistent with search — not 404. |
| Canonical trailing slash | Canonical **with** trailing slash on group roots (`/shop/`); register the bare form to 301 to it. |

These close the 🚩 they name; update each test assertion to the chosen behavior. A few 🚩 in §10 are
**intentionally left open** (exact 4xx code for empty-cart/out-of-stock, optional order-status
filter, `parent_id` validation, cancel-restock policy, security headers) — decide those at build time.

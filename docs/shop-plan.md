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
the `GameService`-style handler factories (`func(ctx) http.HandlerFunc`), GORM models in
`internal/database/models`, and Valkey via `ctx.ValKey`.

---

## 1. How it fits the existing architecture

New/changed packages (mirrors the `games` split of domain vs. HTTP):

```
internal/
├── database/models/
│   └── shop.go              # NEW: Category, Product, ProductImage, Order, OrderItem
│   └── users.go             # CHANGE: add Role field for admin gating
├── shop/                    # NEW: domain layer (pure logic, no net/http)
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

`internal/database/models/shop.go`. Money is stored as **int64 minor units** (cents) plus a
currency code — never floats. Order line items snapshot title/price so historical orders don't
change when a product is later edited.

```go
package models

import "gorm.io/gorm"

// ----- Catalog -----

type ProductStatus string

const (
	ProductDraft     ProductStatus = "draft"
	ProductPublished ProductStatus = "published"
	ProductArchived  ProductStatus = "archived"
)

type Category struct {
	gorm.Model
	Slug        string `json:"slug"        gorm:"uniqueIndex;not null"`
	Name        string `json:"name"        gorm:"not null"`
	Description string `json:"description"`
	ParentID    *uint  `json:"parent_id"   gorm:"index"` // self-referential tree
	Position    int    `json:"position"    gorm:"default:0"`
}

type Product struct {
	gorm.Model
	Slug        string        `json:"slug"        gorm:"uniqueIndex;not null"`
	Title       string        `json:"title"       gorm:"not null"`
	Description string        `json:"description"`
	Status      ProductStatus `json:"status"      gorm:"index;default:draft"`
	PriceCents  int64         `json:"price_cents" gorm:"not null"` // minor units
	Currency    string        `json:"currency"    gorm:"default:USD"`
	SKU         string        `json:"sku"         gorm:"index"`
	StockQty    int           `json:"stock_qty"   gorm:"default:0"`
	Featured    bool          `json:"featured"    gorm:"index;default:false"`

	CategoryID *uint          `json:"category_id" gorm:"index"`
	Category   *Category      `json:"category,omitempty"`
	Images     []ProductImage `json:"images,omitempty"`
}

type ProductImage struct {
	gorm.Model
	ProductID uint   `json:"product_id" gorm:"index;not null"`
	URL       string `json:"url"        gorm:"not null"`
	Alt       string `json:"alt"`
	Position  int    `json:"position"   gorm:"default:0"`
}

// ----- Orders -----

type OrderStatus string

const (
	OrderPendingPayment OrderStatus = "pending_payment"
	OrderPaid           OrderStatus = "paid"
	OrderFulfilled      OrderStatus = "fulfilled"
	OrderCancelled      OrderStatus = "cancelled"
)

type Order struct {
	gorm.Model
	Number   string      `json:"number" gorm:"uniqueIndex"` // human-friendly, e.g. CLR-2K7QF3
	UserID   *uint       `json:"user_id" gorm:"index"`      // nullable => guest checkout
	Email    string      `json:"email"   gorm:"index"`
	Status   OrderStatus `json:"status"  gorm:"index;default:pending_payment"`
	Currency string      `json:"currency"`

	SubtotalCents int64 `json:"subtotal_cents"`
	ShippingCents int64 `json:"shipping_cents"`
	TotalCents    int64 `json:"total_cents"`

	// snapshot of shipping details at purchase time
	ShipName    string `json:"ship_name"`
	ShipAddress string `json:"ship_address"`
	ShipCity    string `json:"ship_city"`
	ShipPostal  string `json:"ship_postal"`
	ShipCountry string `json:"ship_country"`

	Items []OrderItem `json:"items,omitempty"`
}

type OrderItem struct {
	gorm.Model
	OrderID   uint   `json:"order_id"  gorm:"index;not null"`
	ProductID uint   `json:"product_id" gorm:"index"`
	// immutable snapshot — do NOT join back to Product for display
	Title     string `json:"title"`
	SKU       string `json:"sku"`
	UnitCents int64  `json:"unit_cents"`
	Quantity  int    `json:"quantity"`
	LineCents int64  `json:"line_cents"` // UnitCents * Quantity
}
```

Register the new models in `internal/database/database.go` `newSQLiteConn` next to the existing
`db.AutoMigrate(&models.User{})`:

```go
db.AutoMigrate(
	&models.User{},
	&models.Category{},
	&models.Product{},
	&models.ProductImage{},
	&models.Order{},
	&models.OrderItem{},
)
```

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

	"github.com/erancihan/clair/internal/database/models"
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
func (c *Catalog) ListPublished(ctx context.Context, p ListParams) ([]models.Product, int64, error) {
	if p.PerPage <= 0 {
		p.PerPage = 12
	}
	if p.Page <= 0 {
		p.Page = 1
	}

	q := c.tx(ctx).Model(&models.Product{}).Where("status = ?", models.ProductPublished)
	if p.Search != "" {
		q = q.Where("title LIKE ?", "%"+p.Search+"%")
	}
	if p.CategorySlug != "" {
		q = q.Joins("JOIN categories ON categories.id = products.category_id").
			Where("categories.slug = ?", p.CategorySlug)
	}

	var total int64
	q.Count(&total)

	var products []models.Product
	err := q.Preload("Images").
		Offset((p.Page - 1) * p.PerPage).Limit(p.PerPage).
		Order("created_at DESC").Find(&products).Error
	return products, total, err
}

func (c *Catalog) BySlug(ctx context.Context, slug string) (*models.Product, error) {
	var p models.Product
	err := c.tx(ctx).Preload("Images").Preload("Category").
		Where("slug = ? AND status = ?", slug, models.ProductPublished).
		First(&p).Error
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Admin CRUD (unfiltered by status)
func (c *Catalog) AdminList(ctx context.Context) ([]models.Product, error) {
	var ps []models.Product
	err := c.tx(ctx).Preload("Images").Order("updated_at DESC").Find(&ps).Error
	return ps, err
}
func (c *Catalog) Create(ctx context.Context, p *models.Product) error { return c.tx(ctx).Create(p).Error }
func (c *Catalog) Update(ctx context.Context, p *models.Product) error { return c.tx(ctx).Save(p).Error }
func (c *Catalog) Delete(ctx context.Context, id uint) error {
	return c.tx(ctx).Delete(&models.Product{}, id).Error
}
```

### 3.3 Cart (`cart.go` + two stores)

The Valkey client can be **nil** (see `utils.NewValKeyClient`), so the cart is behind an interface
and we pick the implementation at wire time. Cart is keyed by a `cart_id` cookie.

```go
package shop

import "context"

type CartItem struct {
	ProductID uint   `json:"product_id"`
	Slug      string `json:"slug"`
	Title     string `json:"title"`
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

`cart_db.go` mirrors the interface with a `carts` table (`id TEXT PRIMARY KEY, data JSON, updated_at`)
so local dev works with Valkey off. Wire selection:

```go
func NewCartStore(v valkey.Client, db *gorm.DB) CartStore {
	if v != nil {
		return NewValkeyCartStore(v)
	}
	return NewDBCartStore(db)
}
```

### 3.4 Checkout (`checkout.go`)

Converts a cart into an `Order` in a transaction, snapshotting each line and (optionally)
decrementing stock. This is the seam where a real `PaymentProvider` plugs in.

```go
package shop

import (
	"context"

	"github.com/erancihan/clair/internal/database/models"
	"gorm.io/gorm"
)

type ShippingDetails struct {
	Email, Name, Address, City, Postal, Country string
}

func PlaceOrder(ctx context.Context, db *gorm.DB, cart *Cart, s ShippingDetails, orderNumber string) (*models.Order, error) {
	if len(cart.Items) == 0 {
		return nil, ErrEmptyCart
	}

	order := &models.Order{
		Number: orderNumber, Email: s.Email, Status: models.OrderPendingPayment,
		Currency: cart.Currency,
		ShipName: s.Name, ShipAddress: s.Address, ShipCity: s.City,
		ShipPostal: s.Postal, ShipCountry: s.Country,
	}
	for _, it := range cart.Items {
		line := it.UnitCents * int64(it.Quantity)
		order.SubtotalCents += line
		order.Items = append(order.Items, models.OrderItem{
			ProductID: it.ProductID, Title: it.Title, UnitCents: it.UnitCents,
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
			res := tx.Model(&models.Product{}).
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

`orderNumber` should be generated by the HTTP layer (mirroring `utils.GenerateGameID`) so the
domain stays deterministic and testable.

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

### 4.3 Cart cookie + add-to-cart (`cart.go`)

```go
const cartCookie = "cart_id"

func (s *Service) cartID(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(cartCookie); err == nil && c.Value != "" {
		return c.Value
	}
	id := utils.GenerateGameID() // reuse the existing random-id helper
	http.SetCookie(w, &http.Cookie{
		Name: cartCookie, Value: id, Path: "/shop",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 7 * 24 * 3600,
	})
	return id
}

func (s *Service) AddToCart(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := s.cartID(w, r)
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

		cart, _ := s.Carts.Load(r.Context(), id)
		img := ""
		if len(p.Images) > 0 {
			img = p.Images[0].URL
		}
		cart.Upsert(shop.CartItem{
			ProductID: p.ID, Slug: p.Slug, Title: p.Title,
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

```go
shopSvc := shopserver.New(s.context)

mux.Group("shop", func(shop *router.Router) {
	// ---- storefront (public) ----
	shop.HandleFunc("GET /",                 shopSvc.Landing(s.context))
	shop.HandleFunc("GET /products",         shopSvc.ProductList(s.context))
	shop.HandleFunc("GET /products/{slug}",  shopSvc.ProductDetail(s.context))
	shop.HandleFunc("GET /categories/{slug}", shopSvc.CategoryList(s.context))

	// ---- cart ----
	shop.HandleFunc("GET  /cart",         shopSvc.ViewCart(s.context))
	shop.HandleFunc("POST /cart/add",     shopSvc.AddToCart(s.context))
	shop.HandleFunc("POST /cart/update",  shopSvc.UpdateCart(s.context))
	shop.HandleFunc("POST /cart/remove",  shopSvc.RemoveFromCart(s.context))

	// ---- checkout ----
	shop.HandleFunc("GET  /checkout",        shopSvc.Checkout(s.context))
	shop.HandleFunc("POST /checkout",        shopSvc.SubmitCheckout(s.context))
	shop.HandleFunc("GET  /orders/{number}", shopSvc.OrderConfirmation(s.context))

	// ---- CMS (protected) ----
	shop.Middleware(api_auth.AdminMiddleware(s.context)).Group("admin", func(admin *router.Router) {
		admin.HandleFunc("GET  /",                    shopSvc.AdminDashboard(s.context))
		admin.HandleFunc("GET  /products",            shopSvc.AdminProducts(s.context))
		admin.HandleFunc("GET  /products/new",        shopSvc.AdminProductForm(s.context))
		admin.HandleFunc("POST /products",            shopSvc.AdminProductCreate(s.context))
		admin.HandleFunc("GET  /products/{id}/edit",  shopSvc.AdminProductForm(s.context))
		admin.HandleFunc("POST /products/{id}",       shopSvc.AdminProductUpdate(s.context))
		admin.HandleFunc("POST /products/{id}/delete", shopSvc.AdminProductDelete(s.context))
		admin.HandleFunc("POST /products/{id}/images", shopSvc.AdminImageUpload(s.context))

		admin.HandleFunc("GET  /categories",          shopSvc.AdminCategories(s.context))
		admin.HandleFunc("POST /categories",          shopSvc.AdminCategoryCreate(s.context))

		admin.HandleFunc("GET  /orders",              shopSvc.AdminOrders(s.context))
		admin.HandleFunc("GET  /orders/{number}",     shopSvc.AdminOrderDetail(s.context))
		admin.HandleFunc("POST /orders/{number}/status", shopSvc.AdminOrderStatus(s.context))
	})
})
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
	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/shop"
)

templ ProductList(products []models.Product) {
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

templ ProductCard(p models.Product) {
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
templ ProductDetail(p *models.Product) {
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
		p := &models.Product{
			Title:       r.FormValue("title"),
			Slug:        shop.Slugify(r.FormValue("title")),
			Description: r.FormValue("description"),
			PriceCents:  int64(price * 100), // form shows dollars, DB stores cents
			Currency:    "USD",
			Status:      models.ProductStatus(r.FormValue("status")),
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
templ ProductForm(p *models.Product, action string) {
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
				<option value="draft"     selected?={ p.Status == models.ProductDraft }>Draft</option>
				<option value="published" selected?={ p.Status == models.ProductPublished }>Published</option>
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

## 7. Auth & roles (required gap)

The CMS must be gated. `models.User` currently has no role. Add one and an `AdminMiddleware` that
layers on the existing session check.

```go
// models/users.go
type User struct {
	gorm.Model
	ID       uint   `json:"id" gorm:"primaryKey"`
	Username string `json:"username"`
	Email    string `json:"email" gorm:"uniqueIndex"`
	Password string `json:"password"`
	Role     string `json:"role" gorm:"default:customer"` // "customer" | "admin"
}
```

```go
// server/authentication/middleware.go — reuses the session logic, adds a role gate.
func AdminMiddleware(ctx server_context.BackEndContext) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return AuthMiddleware(ctx)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			session, _ := store.Get(r, SESSION_NAME)
			userID, _ := session.Values["id"].(uint)

			var user models.User
			ctx.DBConn.Session(&gorm.Session{Context: r.Context()}).
				Limit(1).Where("id = ?", userID).Find(&user)
			if user.Role != "admin" {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}
```

(Note: `AuthMiddleware` currently returns 401 JSON for browser requests. For the CMS you'll likely
want a redirect to `/login` instead — worth a small `redirectOnFail` variant.)

---

## 8. Image uploads

Simplest path that matches the current static-serving setup: save uploads to the `public/` dir
(served by the existing `GET /public/` route) and store the relative URL in `ProductImage.URL`.

```go
func (s *Service) AdminImageUpload(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		_ = r.ParseMultipartForm(10 << 20) // 10MB
		file, hdr, err := r.FormFile("image")
		if err != nil { http.Error(w, "no file", 400); return }
		defer file.Close()

		name := fmt.Sprintf("products/%d-%s", id, filepath.Base(hdr.Filename))
		dst, _ := os.Create(filepath.Join(web.Public(), name))
		defer dst.Close()
		io.Copy(dst, file)

		s.Catalog.AddImage(r.Context(), uint(id), "/public/"+name, r.FormValue("alt"))
		http.Redirect(w, r, fmt.Sprintf("/shop/admin/products/%d/edit", id), http.StatusSeeOther)
	}
}
```

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

Follow the existing `test/` + `go test ./test/...` setup. Highest-value unit tests (pure domain,
no HTTP):

- `Cart.Upsert` / `SubtotalCents` / `Count` — add, merge, decrement-to-remove.
- `FormatCents` — currencies, negatives, zero-padding.
- `PlaceOrder` — snapshotting, subtotal math, out-of-stock rollback (in-memory SQLite).
- `Slugify` — collisions/uniqueness.

HTTP-level: a `httptest` request through `Routes()` asserting `/shop` renders and `/shop/admin`
returns 403 without an admin session.

---

## 11. Phased milestones

1. **Data + domain** — models, AutoMigrate, `Catalog`, `Money`, `Slugify`, unit tests. No UI.
2. **Storefront read path** — `/shop`, `/shop/products`, `/shop/products/{slug}`, category pages;
   templ views; nav entry. Seed a few products via a tiny CLI/seed to view it.
3. **Cart** — `CartStore` (Valkey + DB fallback), cookie, add/update/remove, cart page, count badge.
4. **Checkout (mock)** — shipping form, `PlaceOrder`, confirmation page, `pending_payment` orders.
5. **Admin CMS** — `Role` + `AdminMiddleware`, product CRUD, image upload, category CRUD, order
   list/detail/status.
6. **Polish** — pagination, search/filter, empty states, 404s, tests, Tailwind `@source`.

Later / additive: real `PaymentProvider` (Stripe), S3 image store, multi-vendor (`Vendor` model +
per-vendor scoping and vendor admin), discounts/coupons, inventory reservations.

---

## 12. Open questions to confirm before build

- Guest checkout allowed, or must a user be logged in to order? (Plan currently allows guest via
  nullable `Order.UserID`.)
- Which currency(ies) at launch? (Plan defaults to single-currency USD.)
- Do you want the CMS auth to **redirect to `/login`** for browsers rather than return 403/401?

// Package aurenor holds the seed catalog for the made-up nation of Aurenor: the
// institutions, schedules and theater showings the booking simulations run on.
//
// catalog.go is deliberately dependency-light — pure data and slug helpers, no
// gorm/models/ticketing imports — so the web layer can reuse it later without
// pulling in the database. seed.go turns this catalog into rows.
package aurenor

import (
	"fmt"
	"strings"
	"time"
)

const (
	// TZ is the schedules' IANA timezone; it must be a loadable location.
	TZ = "Europe/Lisbon"
	// Currency is the seed currency for schedules and tiers.
	Currency = "USD"
	// DefaultHostEmail owns every seeded schedule and event when none is given.
	DefaultHostEmail = "host@aurenor.example"
	// ConsulatePrefix / BureauPrefix are the route-segment slug prefixes for the
	// two single-body institutions (they have no Venue catalog entry).
	ConsulatePrefix = "consulate"
	BureauPrefix    = "bureau"
)

// RuleDef is one recurring weekly availability window. Weekday 0=Sunday..6=Sat;
// StartMin/EndMin are minutes from local midnight (Start inclusive, End exclusive).
type RuleDef struct{ Weekday, StartMin, EndMin int }

// ScheduleDef is one appointment schedule to seed. All seeded schedules are free
// (RequiresPayment=false), so they use the direct /book path.
type ScheduleDef struct {
	Slug, Name, Description                            string
	SlotDurationMin, Capacity, LeadTimeMin, WindowDays int
	Rules                                             []RuleDef
}

// TierDef is a ticket tier to seed under an event. Kind is "ga" (Capacity is the
// pool) or "seated" (Rows*Cols sellable seats in Section).
type TierDef struct {
	Name, Kind, Section string
	PriceCents          int64
	Capacity            int
	Rows, Cols          int
}

// EventDef is one showing (a film at a specific time). Inventory is per-event.
type EventDef struct {
	Slug, Name, Salon string
	DayOffset         int // StartAt = now + DayOffset days at Hour:Minute in TZ
	Hour, Minute      int
	Tier              TierDef
}

// venue pairs a catalog slug with its display name. Duplicated from the web
// Venue catalog on purpose so this package stays dependency-light;
// TestSeedSlugsAlignWithVenueCatalog checks the two agree.
type venue struct{ Slug, Name string }

var hospitalVenues = []venue{
	{"solvarre-general", "Solvarre General Hospital"},
	{"gilt-coast-clinic", "Gilt Coast Clinic"},
}

var theaterVenues = []venue{
	{"grand", "Aurenor Grand Theater"},
	{"harborlight", "Harborlight Cinema"},
}

// Department / service names match the mock apptCard titles verbatim, so Step 2
// can map a card title to its schedule slug with Slugify.
var hospitalDepartments = []string{"Cardiology", "Pediatrics", "General Practice", "Dermatology"}
var consulateServices = []string{"Tourist Visa Interview", "Work Visa Appointment", "Student Visa", "Residence Permit Renewal"}
var bureauServices = []string{"Driver's Licence Renewal", "ID Card Renewal", "Fingerprint Appointment", "Address Registration"}

// mondayToFriday builds a Mon–Fri rule for the given local-minute window.
func mondayToFriday(start, end int) []RuleDef {
	out := make([]RuleDef, 0, 5)
	for wd := 1; wd <= 5; wd++ {
		out = append(out, RuleDef{Weekday: wd, StartMin: start, EndMin: end})
	}
	return out
}

// Slugify lowercases, drops punctuation (apostrophes disappear rather than
// becoming a dash), and joins words with single dashes.
func Slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		case r == ' ' || r == '-' || r == '_':
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// HospitalVenueSlugs / TheaterVenueSlugs expose the catalog's venue slugs for the
// alignment test against the web Venue catalog.
func HospitalVenueSlugs() []string { return venueSlugs(hospitalVenues) }
func TheaterVenueSlugs() []string  { return venueSlugs(theaterVenues) }

func venueSlugs(vs []venue) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Slug
	}
	return out
}

// AllSchedules is every appointment schedule to seed: 8 hospital departments,
// 4 consulate services, 4 bureau services (16 total).
func AllSchedules() []ScheduleDef {
	var out []ScheduleDef

	for _, hv := range hospitalVenues {
		for _, dept := range hospitalDepartments {
			out = append(out, ScheduleDef{
				Slug:            hv.Slug + "-" + Slugify(dept),
				Name:            dept + " · " + hv.Name,
				Description:     "Book a " + dept + " appointment at " + hv.Name + ".",
				SlotDurationMin: 30, Capacity: 1, LeadTimeMin: 120, WindowDays: 30,
				Rules: mondayToFriday(540, 1020), // 09:00–17:00
			})
		}
	}

	for _, svc := range consulateServices {
		out = append(out, ScheduleDef{
			Slug:            ConsulatePrefix + "-" + Slugify(svc),
			Name:            svc + " · Aurenor Consulate",
			Description:     "Book a " + svc + " at the Aurenor Consulate.",
			SlotDurationMin: 30, Capacity: 3, LeadTimeMin: 1440, WindowDays: 45,
			Rules: mondayToFriday(540, 960), // 09:00–16:00
		})
	}

	for _, svc := range bureauServices {
		rules := mondayToFriday(510, 990) // 08:30–16:30
		rules = append(rules, RuleDef{Weekday: 6, StartMin: 540, EndMin: 720}) // Sat 09:00–12:00
		out = append(out, ScheduleDef{
			Slug:            BureauPrefix + "-" + Slugify(svc),
			Name:            svc + " · Aurenor Civic Bureau",
			Description:     "Book a " + svc + " at the Aurenor Civic Bureau.",
			SlotDurationMin: 15, Capacity: 4, LeadTimeMin: 60, WindowDays: 21,
			Rules: rules,
		})
	}

	return out
}

// filmSpec is one film at one theater across several showtimes.
type filmSpec struct {
	Theater string
	Film    string
	Salon   string
	Tier    TierDef
	Times   [][2]int // {hour, minute}
}

func imaxSeated(rows, cols int) TierDef {
	return TierDef{Name: "IMAX", Kind: "seated", Section: "IMAX", PriceCents: 2200, Rows: rows, Cols: cols}
}
func standardSeated(rows, cols int) TierDef {
	return TierDef{Name: "Standard", Kind: "seated", Section: "Stalls", PriceCents: 1800, Rows: rows, Cols: cols}
}
func generalAdmission(capacity int) TierDef {
	return TierDef{Name: "General Admission", Kind: "ga", PriceCents: 1400, Capacity: capacity}
}

var filmSpecs = []filmSpec{
	{"grand", "Dune: Part Two", "Salon 1 · IMAX", imaxSeated(8, 15), [][2]int{{10, 0}, {13, 30}, {19, 0}, {21, 45}}},
	{"grand", "Oppenheimer", "Salon 4", generalAdmission(120), [][2]int{{11, 15}, {15, 0}, {20, 30}}},
	{"grand", "The Northman", "Salon 7", generalAdmission(96), [][2]int{{12, 0}, {18, 15}, {22, 0}}},
	{"grand", "Poor Things", "Salon 2", standardSeated(6, 13), [][2]int{{10, 45}, {14, 20}, {19, 40}}},
	{"harborlight", "Dune: Part Two", "Screen A", generalAdmission(80), [][2]int{{14, 0}, {20, 0}}},
	{"harborlight", "Poor Things", "Screen B", standardSeated(5, 10), [][2]int{{16, 30}, {21, 0}}},
}

// eventSlug is the one place a showing's slug is spelled. The seeder and the
// theater page both go through it, so a page can never offer a showing under a
// name the seeder did not create.
func eventSlug(theater, film string, hour, minute int) string {
	return fmt.Sprintf("%s-%s-%02d%02d", theater, Slugify(film), hour, minute)
}

// Showing is one seeded showing as a page lists it.
//
// Salon, Kind, price and the grid size are carried here rather than read back
// from the API because a page has to draw itself before any of the per-showing
// requests answer: an event row has no salon at all, and knowing the seat grid
// up front is what lets the map reserve its space instead of growing into it.
// The API stays the authority on price and on what is left.
type Showing struct {
	Slug       string `json:"slug"`
	Film       string `json:"film"`
	Salon      string `json:"salon"`
	Kind       string `json:"kind"` // ga | seated
	PriceCents int64  `json:"priceCents"`
	Rows       int    `json:"rows,omitempty"`
	Cols       int    `json:"cols,omitempty"`
}

// TheaterShowings is every showing seeded for one theater, in catalog order.
func TheaterShowings(theater string) []Showing {
	var out []Showing
	for _, fs := range filmSpecs {
		if fs.Theater != theater {
			continue
		}
		for _, hm := range fs.Times {
			out = append(out, Showing{
				Slug:       eventSlug(fs.Theater, fs.Film, hm[0], hm[1]),
				Film:       fs.Film,
				Salon:      fs.Salon,
				Kind:       fs.Tier.Kind,
				PriceCents: fs.Tier.PriceCents,
				Rows:       fs.Tier.Rows,
				Cols:       fs.Tier.Cols,
			})
		}
	}
	return out
}

// FindShowing resolves one theater's showing by its event slug, for the seat
// page that a showtime links to.
func FindShowing(theater, event string) (Showing, bool) {
	for _, s := range TheaterShowings(theater) {
		if s.Slug == event {
			return s, true
		}
	}
	return Showing{}, false
}

// TheaterEventSlugs is TheaterShowings reduced to the slugs the API is called
// with.
func TheaterEventSlugs(theater string) []string {
	showings := TheaterShowings(theater)
	out := make([]string, len(showings))
	for i, s := range showings {
		out[i] = s.Slug
	}
	return out
}

// AllEvents expands every film's showtimes into events, spreading their dates
// over the next week so all are future-dated.
func AllEvents(now time.Time) []EventDef {
	var out []EventDef
	i := 0
	for _, fs := range filmSpecs {
		for _, hm := range fs.Times {
			hour, minute := hm[0], hm[1]
			out = append(out, EventDef{
				Slug:      eventSlug(fs.Theater, fs.Film, hour, minute),
				Name:      fs.Film,
				Salon:     fs.Salon,
				DayOffset: i%7 + 1,
				Hour:      hour,
				Minute:    minute,
				Tier:      fs.Tier,
			})
			i++
		}
	}
	return out
}

// startAt resolves an event's local Hour:Minute on now+DayOffset in TZ.
func startAt(now time.Time, def EventDef) time.Time {
	loc, err := time.LoadLocation(TZ)
	if err != nil {
		loc = time.UTC
	}
	d := now.In(loc).AddDate(0, 0, def.DayOffset)
	return time.Date(d.Year(), d.Month(), d.Day(), def.Hour, def.Minute, 0, 0, loc)
}

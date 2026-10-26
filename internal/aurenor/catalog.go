// Package aurenor holds the seed catalog for the made-up nation of Aurenor: the
// institutions, schedules and theater showings the booking simulations run on.
//
// catalog.go is deliberately dependency-light — pure data and slug helpers, no
// gorm/models/ticketing imports — so the web layer can reuse it later without
// pulling in the database. seed.go turns this catalog into rows.
package aurenor

import (
	"fmt"
	"slices"
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
	// WindowDays is how far ahead the theater calendar runs. The seed and the
	// scheduler's calendar job both keep every showing in it created.
	WindowDays = 14
)

// RuleDef is one recurring weekly availability window. Weekday 0=Sunday..6=Sat;
// StartMin/EndMin are minutes from local midnight (Start inclusive, End exclusive).
type RuleDef struct{ Weekday, StartMin, EndMin int }

// ScheduleDef is one appointment schedule to seed. All seeded schedules are free
// (RequiresPayment=false), so they use the direct /book path.
type ScheduleDef struct {
	Slug, Name, Description                            string
	SlotDurationMin, Capacity, LeadTimeMin, WindowDays int
	Rules                                              []RuleDef
}

// TierDef is a ticket tier to seed under an event. Kind is "ga" (Capacity is the
// pool) or "seated" (Rows*Cols sellable seats in Section).
type TierDef struct {
	Name, Kind, Section string
	PriceCents          int64
	Capacity            int
	Rows, Cols          int
}

// EventDef is one showing: a film at one theater on one date and time.
// Inventory is per event.
type EventDef struct {
	Slug, Theater, Name, Salon string
	StartAt                    time.Time
	Tier                       TierDef
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

// Service is one bookable service: the name a card shows and the line under
// it. The pages read these lists rather than keeping their own, so a service
// is named in exactly one place.
type Service struct{ Name, Blurb string }

var hospitalDepartments = []Service{
	{"Cardiology", "Consultations with the heart & vascular team."},
	{"Pediatrics", "Well-child visits and same-week sick slots."},
	{"General Practice", "Routine check-ups and referrals."},
	{"Dermatology", "Skin, hair and nail consultations."},
}

var consulateServices = []Service{
	{"Tourist Visa Interview", "Short-stay visitor visa appointment."},
	{"Work Visa Appointment", "Employment authorization interview."},
	{"Student Visa", "Study permit application appointment."},
	{"Residence Permit Renewal", "Renew a long-stay residence permit."},
}

var bureauServices = []Service{
	{"Driver's Licence Renewal", "Renew or replace a driver's licence."},
	{"ID Card Renewal", "Renew a national identity card."},
	{"Fingerprint Appointment", "Biometric capture for records requests."},
	{"Address Registration", "Register or update a residential address."},
}

// HospitalDepartments, ConsulateServices and BureauServices return copies, so
// a caller cannot edit the catalog.
func HospitalDepartments() []Service { return slices.Clone(hospitalDepartments) }
func ConsulateServices() []Service   { return slices.Clone(consulateServices) }
func BureauServices() []Service      { return slices.Clone(bureauServices) }

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
		for _, d := range hospitalDepartments {
			dept := d.Name
			out = append(out, ScheduleDef{
				Slug:            hv.Slug + "-" + Slugify(dept),
				Name:            dept + " · " + hv.Name,
				Description:     "Book a " + dept + " appointment at " + hv.Name + ".",
				SlotDurationMin: 30, Capacity: 1, LeadTimeMin: 120, WindowDays: 30,
				Rules: mondayToFriday(540, 1020), // 09:00–17:00
			})
		}
	}

	for _, c := range consulateServices {
		svc := c.Name
		out = append(out, ScheduleDef{
			Slug:            ConsulatePrefix + "-" + Slugify(svc),
			Name:            svc + " · Aurenor Consulate",
			Description:     "Book a " + svc + " at the Aurenor Consulate.",
			SlotDurationMin: 30, Capacity: 3, LeadTimeMin: 1440, WindowDays: 45,
			Rules: mondayToFriday(540, 960), // 09:00–16:00
		})
	}

	for _, b := range bureauServices {
		svc := b.Name
		rules := mondayToFriday(510, 990)                                      // 08:30–16:30
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

// showtime is one weekly slot of a film: the same film, salon and time every
// week on one weekday.
type showtime struct {
	spec         filmSpec
	weekday      time.Weekday
	hour, minute int
}

// showtimes lays every film time out over the week. The n-th time in catalog
// order plays on weekday n mod 7, so each day carries two or three showings and
// a two-week board stays under the summary endpoint's 50-slug cap.
func showtimes() []showtime {
	var out []showtime
	n := 0
	for _, fs := range filmSpecs {
		for _, hm := range fs.Times {
			out = append(out, showtime{spec: fs, weekday: time.Weekday(n % 7), hour: hm[0], minute: hm[1]})
			n++
		}
	}
	return out
}

func location() *time.Location {
	loc, err := time.LoadLocation(TZ)
	if err != nil {
		return time.UTC
	}
	return loc
}

// eventSlug is the one place a showing's slug is spelled. The seeder and the
// theater page both go through it, so a page can never offer a showing under a
// name the seeder did not create. The date is part of the slug because the same
// film plays the same time every week.
func eventSlug(theater, film string, day time.Time, hour, minute int) string {
	return fmt.Sprintf("%s-%s-%s-%02d%02d", theater, Slugify(film), day.Format("20060102"), hour, minute)
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

func showingOf(slug string, fs filmSpec) Showing {
	return Showing{
		Slug: slug, Film: fs.Film, Salon: fs.Salon, Kind: fs.Tier.Kind,
		PriceCents: fs.Tier.PriceCents, Rows: fs.Tier.Rows, Cols: fs.Tier.Cols,
	}
}

// AllEvents is every showing in the window: from now until the end of the
// WindowDays-th local day, in date order. A showing that has already started
// is left out, so the seed never creates one in the past.
func AllEvents(now time.Time) []EventDef {
	loc := location()
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)

	var out []EventDef
	for d := 0; d < WindowDays; d++ {
		day := today.AddDate(0, 0, d)
		for _, st := range showtimes() {
			if st.weekday != day.Weekday() {
				continue
			}
			start := time.Date(day.Year(), day.Month(), day.Day(), st.hour, st.minute, 0, 0, loc)
			if !start.After(now) {
				continue
			}
			out = append(out, EventDef{
				Slug:    eventSlug(st.spec.Theater, st.spec.Film, day, st.hour, st.minute),
				Theater: st.spec.Theater,
				Name:    st.spec.Film,
				Salon:   st.spec.Salon,
				StartAt: start,
				Tier:    st.spec.Tier,
			})
		}
	}
	return out
}

// TheaterShowings is every showing one theater has in the window, in date
// order.
func TheaterShowings(theater string, now time.Time) []Showing {
	var out []Showing
	byFilm := map[string]filmSpec{}
	for _, fs := range filmSpecs {
		byFilm[fs.Theater+"|"+fs.Film] = fs
	}
	for _, e := range AllEvents(now) {
		if e.Theater != theater {
			continue
		}
		out = append(out, showingOf(e.Slug, byFilm[e.Theater+"|"+e.Name]))
	}
	return out
}

// FindShowing resolves one theater's showing by its event slug, for the seat
// page that a showtime links to. It reads the date out of the slug rather than
// looking in the window, so a link opened a day late still finds its showing;
// the API then says whether it can still be sold.
func FindShowing(theater, event string) (Showing, bool) {
	loc := location()
	for _, st := range showtimes() {
		if st.spec.Theater != theater {
			continue
		}
		prefix := theater + "-" + Slugify(st.spec.Film) + "-"
		suffix := fmt.Sprintf("-%02d%02d", st.hour, st.minute)
		if !strings.HasPrefix(event, prefix) || !strings.HasSuffix(event, suffix) {
			continue
		}
		date := strings.TrimSuffix(strings.TrimPrefix(event, prefix), suffix)
		day, err := time.ParseInLocation("20060102", date, loc)
		if err != nil || day.Weekday() != st.weekday {
			continue
		}
		return showingOf(event, st.spec), true
	}
	return Showing{}, false
}

// TheaterEventSlugs is TheaterShowings reduced to the slugs the API is called
// with.
func TheaterEventSlugs(theater string, now time.Time) []string {
	showings := TheaterShowings(theater, now)
	out := make([]string, len(showings))
	for i, s := range showings {
		out[i] = s.Slug
	}
	return out
}

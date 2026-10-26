package pages

import (
	"encoding/json"

	"github.com/erancihan/clair/internal/aurenor"
)

// apptCard is one bookable in an appointment grid (a department, a service).
//
// ScheduleSlug is the API path segment the card books against, and it is never
// written by hand: it is derived from the card title with the same aurenor
// helpers the seeder uses, so a renamed service can't leave a button pointing at
// a schedule that was never created.
type apptCard struct {
	Title        string
	Blurb        string
	ScheduleSlug string
}

// hospitalDepartments are the departments every hospital offers, each booking
// that hospital's own inventory — two hospitals never share a waiting list.
func hospitalDepartments(v Venue) []apptCard {
	return []apptCard{
		venueCard(v, "Cardiology", "Consultations with the heart & vascular team."),
		venueCard(v, "Pediatrics", "Well-child visits and same-week sick slots."),
		venueCard(v, "General Practice", "Routine check-ups and referrals."),
		venueCard(v, "Dermatology", "Skin, hair and nail consultations."),
	}
}

// aurenorConsulateServices and aurenorBureauServices are the two single-body
// institutions' catalogs. They have no Venue, so their slugs carry a fixed
// institution prefix instead.
var aurenorConsulateServices = []apptCard{
	prefixedCard(aurenor.ConsulatePrefix, "Tourist Visa Interview", "Short-stay visitor visa appointment."),
	prefixedCard(aurenor.ConsulatePrefix, "Work Visa Appointment", "Employment authorization interview."),
	prefixedCard(aurenor.ConsulatePrefix, "Student Visa", "Study permit application appointment."),
	prefixedCard(aurenor.ConsulatePrefix, "Residence Permit Renewal", "Renew a long-stay residence permit."),
}

var aurenorBureauServices = []apptCard{
	prefixedCard(aurenor.BureauPrefix, "Driver's Licence Renewal", "Renew or replace a driver's licence."),
	prefixedCard(aurenor.BureauPrefix, "ID Card Renewal", "Renew a national identity card."),
	prefixedCard(aurenor.BureauPrefix, "Fingerprint Appointment", "Biometric capture for records requests."),
	prefixedCard(aurenor.BureauPrefix, "Address Registration", "Register or update a residential address."),
}

func venueCard(v Venue, title, blurb string) apptCard {
	return prefixedCard(v.Slug, title, blurb)
}

func prefixedCard(prefix, title, blurb string) apptCard {
	return apptCard{Title: title, Blurb: blurb, ScheduleSlug: prefix + "-" + aurenor.Slugify(title)}
}

// ServiceCard is one bookable service on its own screen: which institution it
// belongs to, and which schedule it books.
type ServiceCard struct {
	Institution  string // "hospitals" | "consulate" | "bureau"
	Venue        string // hospital slug; empty for the single-body institutions
	Slug         string // "cardiology"
	Title        string
	Blurb        string
	ScheduleSlug string
}

// ServicesFor lists an institution's services. The hospital list is per venue
// because each hospital books its own inventory.
func ServicesFor(institution, venue string) []ServiceCard {
	switch institution {
	case "hospitals":
		v, ok := FindHospital(venue)
		if !ok {
			return nil
		}
		return serviceCards("hospitals", v.Slug, hospitalDepartments(v))
	case "consulate":
		return serviceCards("consulate", "", aurenorConsulateServices)
	case "bureau":
		return serviceCards("bureau", "", aurenorBureauServices)
	}
	return nil
}

func serviceCards(institution, venue string, cards []apptCard) []ServiceCard {
	out := make([]ServiceCard, len(cards))
	for i, c := range cards {
		out[i] = ServiceCard{
			Institution: institution, Venue: venue,
			Slug:  aurenor.Slugify(c.Title),
			Title: c.Title, Blurb: c.Blurb, ScheduleSlug: c.ScheduleSlug,
		}
	}
	return out
}

// FindService resolves one service screen.
func FindService(institution, venue, service string) (ServiceCard, bool) {
	for _, card := range ServicesFor(institution, venue) {
		if card.Slug == service {
			return card, true
		}
	}
	return ServiceCard{}, false
}

// ServiceHref is where a service's screen lives.
func ServiceHref(card ServiceCard) string {
	if card.Venue != "" {
		return "/aurenor/" + card.Institution + "/" + card.Venue + "/" + card.Slug
	}
	return "/aurenor/" + card.Institution + "/" + card.Slug
}

// serviceBackHref is the institution a service belongs to.
func serviceBackHref(card ServiceCard) string {
	if card.Venue != "" {
		return "/aurenor/" + card.Institution + "/" + card.Venue
	}
	return "/aurenor/" + card.Institution
}

// serviceAttr binds a service screen to its schedule.
func serviceAttr(card ServiceCard) string {
	cfg, _ := json.Marshal(map[string]string{
		"slug":     card.ScheduleSlug,
		"title":    card.Title,
		"backHref": serviceBackHref(card),
	})
	return "service(" + string(cfg) + ")"
}

// AppointmentScheduleSlugs is every schedule slug the Aurenor pages can render.
// Exported for the contract test that checks each one is a schedule the seeder
// actually creates.
func AppointmentScheduleSlugs() []string {
	var cards []apptCard
	for _, v := range aurenorHospitals {
		cards = append(cards, hospitalDepartments(v)...)
	}
	cards = append(cards, aurenorConsulateServices...)
	cards = append(cards, aurenorBureauServices...)

	out := make([]string, len(cards))
	for i, c := range cards {
		out[i] = c.ScheduleSlug
	}
	return out
}

// theaterAttr builds the x-data expression for a theater's showings board.
//
// The catalog is the source of the slug list because there is no endpoint that
// lists an event collection; the component fetches each showing by slug from
// here.
func theaterAttr(v Venue) string {
	showings, _ := json.Marshal(aurenor.TheaterShowings(v.Slug))
	return "theater(" + string(showings) + ")"
}

// showingAttr builds the x-data expression for one showing's seat screen. The
// catalog fields let the page render its heading and the right kind of picker
// before the event request answers.
func showingAttr(v Venue, s aurenor.Showing) string {
	cfg, _ := json.Marshal(map[string]any{
		"slug":       s.Slug,
		"film":       s.Film,
		"salon":      s.Salon,
		"kind":       s.Kind,
		"priceCents": s.PriceCents,
		"backHref":   "/aurenor/theaters/" + v.Slug,
	})
	return "showing(" + string(cfg) + ")"
}

// apptBookerAttr builds the x-data expression binding one card to its schedule.
//
// The argument is JSON rather than quoted literals because service titles carry
// apostrophes ("Driver's Licence Renewal"); templ escapes the attribute and the
// browser hands Alpine back the original text.
func apptBookerAttr(c apptCard) string {
	arg, _ := json.Marshal(map[string]string{"slug": c.ScheduleSlug, "title": c.Title})
	return "apptBooker(" + string(arg) + ")"
}

// Venue is one institution instance in a collection (a theater, a hospital).
// Exported so the mount seam can resolve a {slug} to the venue a detail page
// renders.
type Venue struct {
	Slug     string
	Name     string
	Location string
	Blurb    string
}

// aurenorTheaters and aurenorHospitals are the mock institution catalogs. The
// collection URL scheme (/aurenor/theaters, /aurenor/hospitals) exists to hold
// more than one of each; these lists are where new instances are added.
var aurenorTheaters = []Venue{
	{Slug: "grand", Name: "Aurenor Grand Theater", Location: "Solvarre · Gilt Coast", Blurb: "Ten salons, flagship of the capital."},
	{Slug: "harborlight", Name: "Harborlight Cinema", Location: "Marisk", Blurb: "Three screens overlooking the sound."},
}

var aurenorHospitals = []Venue{
	{Slug: "solvarre-general", Name: "Solvarre General Hospital", Location: "Solvarre", Blurb: "The capital's teaching hospital."},
	{Slug: "gilt-coast-clinic", Name: "Gilt Coast Clinic", Location: "Aurevik", Blurb: "A coastal outpatient clinic."},
}

// FindTheater / FindHospital resolve a slug to its Venue for the detail pages.
func FindTheater(slug string) (Venue, bool)  { return findVenue(aurenorTheaters, slug) }
func FindHospital(slug string) (Venue, bool) { return findVenue(aurenorHospitals, slug) }

func findVenue(list []Venue, slug string) (Venue, bool) {
	for _, v := range list {
		if v.Slug == slug {
			return v, true
		}
	}
	return Venue{}, false
}

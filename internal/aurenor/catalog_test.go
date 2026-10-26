package aurenor

import (
	"testing"
	"time"
)

// The catalog's sizes are asserted exactly, so a dropped rule or a lost
// showtime fails here rather than as a thinner page.
const (
	wantSchedules = 16 // 8 hospital departments, 4 consulate, 4 bureau
	wantRules     = 84 // 12 Mon–Fri schedules × 5 + 4 bureau schedules × 6
	wantShowtimes = 17 // weekly film times across both theaters
)

func TestScheduleSlugsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	rules := 0
	for _, s := range AllSchedules() {
		if seen[s.Slug] {
			t.Errorf("duplicate schedule slug %q", s.Slug)
		}
		seen[s.Slug] = true
		rules += len(s.Rules)
	}
	if len(seen) != wantSchedules {
		t.Errorf("want %d schedules, got %d", wantSchedules, len(seen))
	}
	if rules != wantRules {
		t.Errorf("want %d rules, got %d", wantRules, rules)
	}
}

// localMidnight is the start of a day in the catalog's timezone.
func localMidnight(t *testing.T, y int, m time.Month, d int) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(TZ)
	if err != nil {
		t.Fatal(err)
	}
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

func TestEventSlugsAreUnique(t *testing.T) {
	now := localMidnight(t, 2026, 6, 1)
	seen := map[string]bool{}
	for _, e := range AllEvents(now) {
		if seen[e.Slug] {
			t.Errorf("duplicate event slug %q", e.Slug)
		}
		seen[e.Slug] = true
	}
	if n := len(showtimes()); n != wantShowtimes {
		t.Errorf("want %d weekly showtimes, got %d", wantShowtimes, n)
	}
	// From midnight, every weekday occurs exactly twice in a 14-day window.
	if want := wantShowtimes * WindowDays / 7; len(seen) != want {
		t.Errorf("want %d events in the window, got %d", want, len(seen))
	}
}

// TestBoardFitsOneSummaryRequest: a theater board asks for every showing in
// its window in one request, and the summary endpoint takes at most 50 slugs.
func TestBoardFitsOneSummaryRequest(t *testing.T) {
	now := localMidnight(t, 2026, 6, 1)
	for _, theater := range TheaterVenueSlugs() {
		if n := len(TheaterShowings(theater, now)); n == 0 || n > 50 {
			t.Errorf("theater %q lists %d showings, want 1..50", theater, n)
		}
	}
}

// TestWindowRolls is O11: a window computed a week later still holds a full
// fortnight of future showings, and the days that overlap keep their slugs.
func TestWindowRolls(t *testing.T) {
	first := localMidnight(t, 2026, 6, 1)
	later := first.AddDate(0, 0, 8).Add(time.Hour)

	before := map[string]time.Time{}
	for _, e := range AllEvents(first) {
		before[e.Slug] = e.StartAt
	}

	events := AllEvents(later)
	if len(events) == 0 {
		t.Fatal("no showings eight days on")
	}
	horizon := later.AddDate(0, 0, WindowDays)
	shared := 0
	for _, e := range events {
		if !e.StartAt.After(later) {
			t.Errorf("%s starts %v, not after now %v", e.Slug, e.StartAt, later)
		}
		if !e.StartAt.Before(horizon) {
			t.Errorf("%s starts %v, past the window", e.Slug, e.StartAt)
		}
		if at, ok := before[e.Slug]; ok {
			shared++
			if !at.Equal(e.StartAt) {
				t.Errorf("%s moved from %v to %v", e.Slug, at, e.StartAt)
			}
		}
	}
	if shared == 0 {
		t.Error("windows a week apart share no showing; slugs are not stable")
	}
}

func TestFindShowingReadsTheSlug(t *testing.T) {
	now := localMidnight(t, 2026, 6, 1)
	for _, e := range AllEvents(now) {
		s, ok := FindShowing(e.Theater, e.Slug)
		if !ok {
			t.Errorf("FindShowing(%q, %q) found nothing", e.Theater, e.Slug)
			continue
		}
		if s.Film != e.Name {
			t.Errorf("%s resolved to film %q, want %q", e.Slug, s.Film, e.Name)
		}
	}

	e := AllEvents(now)[0]
	for _, bad := range []string{
		e.Slug + "x",
		"grand-dune-part-two-1000",          // the old undated form
		"grand-dune-part-two-20260602-1000", // a Tuesday; this time plays another day
		"grand-dune-part-two-2026060x-1000",
	} {
		if _, ok := FindShowing(e.Theater, bad); ok && bad != e.Slug {
			t.Errorf("FindShowing accepted %q", bad)
		}
	}
}

func TestRuleBounds(t *testing.T) {
	for _, s := range AllSchedules() {
		if len(s.Rules) == 0 {
			t.Errorf("schedule %q has no rules", s.Slug)
		}
		for _, r := range s.Rules {
			if r.Weekday < 0 || r.Weekday > 6 {
				t.Errorf("%q: weekday %d out of range", s.Slug, r.Weekday)
			}
			if r.StartMin < 0 || r.StartMin >= r.EndMin || r.EndMin > 1440 {
				t.Errorf("%q: bad window %d..%d", s.Slug, r.StartMin, r.EndMin)
			}
		}
	}
}

func TestScheduleCapacities(t *testing.T) {
	for _, s := range AllSchedules() {
		if s.Capacity < 1 {
			t.Errorf("schedule %q capacity %d < 1", s.Slug, s.Capacity)
		}
		if s.SlotDurationMin < 1 {
			t.Errorf("schedule %q slot duration %d < 1", s.Slug, s.SlotDurationMin)
		}
	}
}

func TestTierCapacities(t *testing.T) {
	for _, e := range AllEvents(localMidnight(t, 2026, 6, 1)) {
		switch e.Tier.Kind {
		case "ga":
			if e.Tier.Capacity < 1 {
				t.Errorf("event %q GA capacity %d < 1", e.Slug, e.Tier.Capacity)
			}
		case "seated":
			if e.Tier.Rows < 1 || e.Tier.Cols < 1 {
				t.Errorf("event %q seated grid %dx%d invalid", e.Slug, e.Tier.Rows, e.Tier.Cols)
			}
		default:
			t.Errorf("event %q has invalid tier kind %q", e.Slug, e.Tier.Kind)
		}
	}
}

func TestTimezoneLoads(t *testing.T) {
	if _, err := time.LoadLocation(TZ); err != nil {
		t.Fatalf("TZ %q is not a loadable IANA location: %v", TZ, err)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Driver's Licence Renewal": "drivers-licence-renewal",
		"Dune: Part Two":           "dune-part-two",
		"General Practice":         "general-practice",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

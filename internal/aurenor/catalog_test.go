package aurenor

import (
	"testing"
	"time"
)

func TestScheduleSlugsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range AllSchedules() {
		if seen[s.Slug] {
			t.Errorf("duplicate schedule slug %q", s.Slug)
		}
		seen[s.Slug] = true
	}
	if len(seen) != 16 {
		t.Errorf("want 16 schedules, got %d", len(seen))
	}
}

func TestEventSlugsAreUnique(t *testing.T) {
	fixedNow := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	seen := map[string]bool{}
	for _, e := range AllEvents(fixedNow) {
		if seen[e.Slug] {
			t.Errorf("duplicate event slug %q", e.Slug)
		}
		seen[e.Slug] = true
	}
	if len(seen) == 0 {
		t.Fatal("no events defined")
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
	fixedNow := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	for _, e := range AllEvents(fixedNow) {
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

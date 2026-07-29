package appointments_test

import (
	"testing"
	"time"

	"github.com/erancihan/clair/internal/appointments"
	"github.com/erancihan/clair/internal/database/models"
)

func iptr(v int) *int { return &v }

func mustUTC(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

// A schedule with 30-minute slots and a Mon 09:00–12:00 rule, in UTC.
func mondaySchedule() (*models.BookingSchedule, []models.BookingAvailabilityRule) {
	sched := &models.BookingSchedule{
		Timezone: "UTC", SlotDurationMin: 30, Capacity: 1, WindowDays: 365,
	}
	rules := []models.BookingAvailabilityRule{
		{Weekday: int(time.Monday), StartMin: 9 * 60, EndMin: 12 * 60},
	}
	return sched, rules
}

func TestComputeAvailability_Basic(t *testing.T) {
	sched, rules := mondaySchedule()
	past := mustUTC(t, "2026-01-01T00:00:00Z")
	from := mustUTC(t, "2026-03-02T00:00:00Z") // Monday
	to := mustUTC(t, "2026-03-03T00:00:00Z")

	slots, err := appointments.ComputeAvailability(sched, rules, nil, nil, from, to, past)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 6 {
		t.Fatalf("want 6 slots (09:00–11:30), got %d", len(slots))
	}
	if !slots[0].StartAt.Equal(mustUTC(t, "2026-03-02T09:00:00Z")) {
		t.Fatalf("first slot = %s, want 09:00Z", slots[0].StartAt)
	}
	if !slots[5].StartAt.Equal(mustUTC(t, "2026-03-02T11:30:00Z")) {
		t.Fatalf("last slot = %s, want 11:30Z", slots[5].StartAt)
	}
}

func TestComputeAvailability_WholeDayException(t *testing.T) {
	sched, rules := mondaySchedule()
	past := mustUTC(t, "2026-01-01T00:00:00Z")
	from := mustUTC(t, "2026-03-02T00:00:00Z")
	to := mustUTC(t, "2026-03-03T00:00:00Z")
	exs := []models.BookingAvailabilityException{{Date: "2026-03-02"}} // nil min => whole day

	slots, err := appointments.ComputeAvailability(sched, rules, exs, nil, from, to, past)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 0 {
		t.Fatalf("whole-day block should yield 0 slots, got %d", len(slots))
	}
}

func TestComputeAvailability_PartialException(t *testing.T) {
	sched, rules := mondaySchedule()
	past := mustUTC(t, "2026-01-01T00:00:00Z")
	from := mustUTC(t, "2026-03-02T00:00:00Z")
	to := mustUTC(t, "2026-03-03T00:00:00Z")
	// Block 09:00–10:00 → removes the 09:00 and 09:30 slots, keeps 10:00–11:30.
	exs := []models.BookingAvailabilityException{{Date: "2026-03-02", StartMin: iptr(9 * 60), EndMin: iptr(10 * 60)}}

	slots, err := appointments.ComputeAvailability(sched, rules, exs, nil, from, to, past)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 4 {
		t.Fatalf("want 4 slots after partial block, got %d", len(slots))
	}
	if !slots[0].StartAt.Equal(mustUTC(t, "2026-03-02T10:00:00Z")) {
		t.Fatalf("first slot = %s, want 10:00Z", slots[0].StartAt)
	}
}

func TestComputeAvailability_LeadTime(t *testing.T) {
	sched, rules := mondaySchedule()
	from := mustUTC(t, "2026-03-02T00:00:00Z")
	to := mustUTC(t, "2026-03-03T00:00:00Z")
	// now is 10:15 on the day itself → only 10:30, 11:00, 11:30 remain bookable.
	now := mustUTC(t, "2026-03-02T10:15:00Z")

	slots, err := appointments.ComputeAvailability(sched, rules, nil, nil, from, to, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 3 {
		t.Fatalf("want 3 slots after lead-time cut, got %d", len(slots))
	}
	if !slots[0].StartAt.Equal(mustUTC(t, "2026-03-02T10:30:00Z")) {
		t.Fatalf("first bookable = %s, want 10:30Z", slots[0].StartAt)
	}
}

func TestComputeAvailability_WindowClamp(t *testing.T) {
	sched, rules := mondaySchedule()
	sched.WindowDays = 0 // nothing bookable beyond "now"
	now := mustUTC(t, "2026-03-01T00:00:00Z")
	from := mustUTC(t, "2026-03-02T00:00:00Z")
	to := mustUTC(t, "2026-03-03T00:00:00Z")

	slots, err := appointments.ComputeAvailability(sched, rules, nil, nil, from, to, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 0 {
		t.Fatalf("window=0 should yield 0 future slots, got %d", len(slots))
	}
}

func TestComputeAvailability_FullyBookedExcluded(t *testing.T) {
	sched, rules := mondaySchedule()
	past := mustUTC(t, "2026-01-01T00:00:00Z")
	from := mustUTC(t, "2026-03-02T00:00:00Z")
	to := mustUTC(t, "2026-03-03T00:00:00Z")
	booked := map[time.Time]int{
		mustUTC(t, "2026-03-02T09:00:00Z"): 1, // capacity 1 → this slot is full
	}

	slots, err := appointments.ComputeAvailability(sched, rules, nil, booked, from, to, past)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 5 {
		t.Fatalf("want 5 slots (09:00 fully booked), got %d", len(slots))
	}
	if !slots[0].StartAt.Equal(mustUTC(t, "2026-03-02T09:30:00Z")) {
		t.Fatalf("first slot = %s, want 09:30Z (09:00 taken)", slots[0].StartAt)
	}
}

// The load-bearing correctness case: a DST spring-forward day must skip the
// non-existent local hour and keep the right UTC offsets on either side.
func TestComputeAvailability_DSTSpringForward(t *testing.T) {
	sched := &models.BookingSchedule{
		Timezone: "America/New_York", SlotDurationMin: 60, Capacity: 1, WindowDays: 365,
	}
	// US DST 2026 begins Sun Mar 8 at 02:00 local (→ 03:00). Rule 01:00–04:00.
	rules := []models.BookingAvailabilityRule{
		{Weekday: int(time.Sunday), StartMin: 1 * 60, EndMin: 4 * 60},
	}
	past := mustUTC(t, "2026-01-01T00:00:00Z")
	from := mustUTC(t, "2026-03-08T00:00:00Z")
	to := mustUTC(t, "2026-03-09T12:00:00Z")

	slots, err := appointments.ComputeAvailability(sched, rules, nil, nil, from, to, past)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 2 {
		t.Fatalf("spring-forward day: want 2 slots (01:00 EST, 03:00 EDT; 02:00 skipped), got %d: %v", len(slots), slots)
	}
	// 01:00 EST = 06:00Z, 03:00 EDT = 07:00Z.
	if !slots[0].StartAt.Equal(mustUTC(t, "2026-03-08T06:00:00Z")) {
		t.Fatalf("slot[0] = %s, want 06:00Z", slots[0].StartAt)
	}
	if !slots[1].StartAt.Equal(mustUTC(t, "2026-03-08T07:00:00Z")) {
		t.Fatalf("slot[1] = %s, want 07:00Z", slots[1].StartAt)
	}
}

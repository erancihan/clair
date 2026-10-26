// Package simulator makes the Aurenor booking demos look lived in. It books
// showings and appointment slots through the same in-process domain calls a
// visitor's request reaches, so every row it writes is one the booking service
// itself decided to allow.
//
// Each tick nudges every showing and slot in the horizon toward a target
// occupancy that rises as its start approaches (the demand curve), cancels a
// few of its own paid orders and appointments (churn, through the B2 cancel
// path), and places a few holds it never pays for (abandoned carts, which the
// reaper releases). The simulator's buyers own references in the "sim:"
// namespace, so its rows can always be told apart from a visitor's.
package simulator

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/erancihan/clair/internal/appointments"
	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/ticketing"
	"gorm.io/gorm"
)

// OwnerPrefix is the namespace of every owner reference the simulator books
// under. Appointments carry no owner reference, so the simulator writes the
// same prefix into their guest email instead.
const OwnerPrefix = "sim:"

// Config tunes the simulation. The zero value is not useful; start from
// DefaultConfig.
type Config struct {
	// TargetOccupancy is the share of a showing or slot that is taken by the
	// time it starts, 0 to 1.
	TargetOccupancy float64
	// Horizon is how far ahead the simulator books. Further out, nothing moves.
	Horizon time.Duration
	// MaxUnitsPerShowing caps how many seats or tickets one tick adds to one
	// showing, so a fresh showing fills over hours rather than in one burst.
	MaxUnitsPerShowing int
	// SlotsPerSchedule is how many open slots of each schedule one tick looks
	// at.
	SlotsPerSchedule int
	// CancelRate is the chance per showing (and per schedule) per tick that one
	// of the simulator's own bookings there is cancelled.
	CancelRate float64
	// AbandonRate is the chance per showing per tick of a hold that is never
	// paid for.
	AbandonRate float64
	// AbandonTTL is how long an abandoned hold lives before the reaper takes it.
	AbandonTTL time.Duration
}

// DefaultConfig is a gentle simulation: showings reach 60% by their start, a
// tick adds at most three seats to a showing, and churn is low.
func DefaultConfig() Config {
	return Config{
		TargetOccupancy:    0.6,
		Horizon:            14 * 24 * time.Hour,
		MaxUnitsPerShowing: 3,
		SlotsPerSchedule:   3,
		CancelRate:         0.05,
		AbandonRate:        0.05,
		AbandonTTL:         3 * time.Minute,
	}
}

// Report counts what one tick did.
type Report struct {
	TicketsSold         int
	AppointmentsBooked  int
	OrdersCancelled     int
	AppointmentsDropped int
	HoldsAbandoned      int
}

// Simulator runs ticks against one database. It is not safe for concurrent
// ticks; the Runner's lock is what keeps one tick at a time across processes.
type Simulator struct {
	db  *gorm.DB
	cfg Config
	rng *rand.Rand
	seq atomic.Uint64
}

// New builds a simulator. seed makes a run reproducible, which is what the
// tests rely on.
func New(db *gorm.DB, cfg Config, seed uint64) *Simulator {
	return &Simulator{db: db, cfg: cfg, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))}
}

// desired is the demand curve: the share of a unit that should be taken given
// how far off its start is. Zero at the horizon, the target at the start, and
// rising fastest early, the way a showing sells.
func (s *Simulator) desired(until time.Duration) float64 {
	if until <= 0 {
		return s.cfg.TargetOccupancy
	}
	progress := 1 - float64(until)/float64(s.cfg.Horizon)
	if progress <= 0 {
		return 0
	}
	return s.cfg.TargetOccupancy * math.Sqrt(progress)
}

// owner mints a fresh simulated buyer. One per purchase, so the per-buyer cap
// never decides how full a showing gets.
func (s *Simulator) owner(now time.Time) string {
	return fmt.Sprintf("%s%d-%d", OwnerPrefix, now.UnixNano(), s.seq.Add(1))
}

// Tick runs one round over every showing and schedule in the horizon. Errors
// from one showing or slot do not stop the round; they are joined and
// returned with the report of what did happen.
func (s *Simulator) Tick(ctx context.Context, now time.Time) (Report, error) {
	var rep Report
	var errs []error
	db := s.db.WithContext(ctx)

	var events []models.BookingEvent
	if err := db.Where("status = 'scheduled' AND start_at > ? AND start_at <= ?", now, now.Add(s.cfg.Horizon)).
		Order("start_at").Find(&events).Error; err != nil {
		return rep, err
	}
	for _, ev := range events {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if err := s.showing(db, ev, now, &rep); err != nil {
			errs = append(errs, fmt.Errorf("showing %s: %w", ev.Slug, err))
		}
	}

	var schedules []models.BookingSchedule
	if err := db.Where("active AND NOT requires_payment").Order("id").Find(&schedules).Error; err != nil {
		return rep, err
	}
	for i := range schedules {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if err := s.schedule(db, &schedules[i], now, &rep); err != nil {
			errs = append(errs, fmt.Errorf("schedule %s: %w", schedules[i].Slug, err))
		}
	}
	return rep, errors.Join(errs...)
}

// occupancy is the share of an event's sellable units that are booked or held.
func occupancy(db *gorm.DB, eventID uint) (float64, int, error) {
	var sums struct{ Taken, Capacity int }
	err := db.Raw(`
		SELECT COALESCE(SUM(i.booked + i.held), 0) AS taken, COALESCE(SUM(i.capacity - i.blocked), 0) AS capacity
		FROM booking_inventories i
		LEFT JOIN booking_seats st ON st.id = i.seat_id
		JOIN booking_ticket_tiers t ON t.id = COALESCE(i.tier_id, st.tier_id)
		WHERE t.event_id = ? AND (st.id IS NULL OR st.kind = 'sellable')`, eventID).Scan(&sums).Error
	if err != nil || sums.Capacity == 0 {
		return 0, 0, err
	}
	return float64(sums.Taken) / float64(sums.Capacity), sums.Capacity, nil
}

func (s *Simulator) showing(db *gorm.DB, ev models.BookingEvent, now time.Time, rep *Report) error {
	var tier models.BookingTicketTier
	if res := db.Where("event_id = ?", ev.ID).Order("id").Limit(1).Find(&tier); res.Error != nil || res.RowsAffected == 0 {
		return res.Error
	}

	if s.rng.Float64() < s.cfg.CancelRate {
		cancelled, err := s.cancelOne(db, ev.ID, now)
		if err != nil {
			return err
		}
		if cancelled {
			rep.OrdersCancelled++
		}
	}

	if s.rng.Float64() < s.cfg.AbandonRate {
		ok, err := s.buy(db, ev, tier, 1, now, false)
		if err != nil {
			return err
		}
		if ok {
			rep.HoldsAbandoned++
		}
	}

	occ, capacity, err := occupancy(db, ev.ID)
	if err != nil || capacity == 0 {
		return err
	}
	want := s.desired(ev.StartAt.Sub(now))
	gap := int(math.Floor((want - occ) * float64(capacity)))
	if gap <= 0 {
		return nil
	}
	n := min(gap, s.cfg.MaxUnitsPerShowing)
	if ev.MaxPerBuyer > 0 {
		n = min(n, ev.MaxPerBuyer)
	}
	ok, err := s.buy(db, ev, tier, n, now, true)
	if err != nil {
		return err
	}
	if ok {
		rep.TicketsSold += n
	}
	return nil
}

// buy holds n units of a showing for a fresh simulated buyer and, when pay is
// set, checks out. A unit taken by someone else first is not an error: the
// showing is simply fuller than the simulator thought.
func (s *Simulator) buy(db *gorm.DB, ev models.BookingEvent, tier models.BookingTicketTier, n int, now time.Time, pay bool) (bool, error) {
	buyer := ticketing.Buyer{OwnerRef: s.owner(now), Name: "Aurenori guest"}
	ttl := 10 * time.Minute
	if !pay {
		ttl = s.cfg.AbandonTTL
	}

	var err error
	switch tier.Kind {
	case "ga":
		_, _, err = ticketing.HoldGA(db, &tier, n, buyer, ttl, now, ev.MaxPerBuyer)
	case "seated":
		seats, ferr := s.freeRun(db, tier.ID, n)
		if ferr != nil {
			return false, ferr
		}
		if len(seats) == 0 {
			return false, nil
		}
		_, _, err = ticketing.HoldSeatGroup(db, ev.ID, seats, buyer, ttl, now, ev.MaxPerBuyer)
	default:
		return false, nil
	}
	if busy(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !pay {
		return true, nil
	}
	_, err = ticketing.Checkout(db, ev.ID, buyer, now)
	if errors.Is(err, ticketing.ErrHoldLapsed) {
		return false, nil
	}
	return err == nil, err
}

// busy is the set of refusals a real buyer gets too and simply tries again
// after: somebody else was faster, or the showing closed.
func busy(err error) bool {
	return errors.Is(err, booking.ErrSoldOut) || errors.Is(err, ticketing.ErrPurchaseLimit) ||
		errors.Is(err, ticketing.ErrEventClosed)
}

// freeRun picks up to n free sellable seats side by side in one row, starting
// from a random free seat, the way a group sits together.
func (s *Simulator) freeRun(db *gorm.DB, tierID uint, n int) ([]uint, error) {
	type seat struct {
		ID      uint
		RowName string
		Number  int
	}
	var free []seat
	if err := db.Raw(`
		SELECT st.id, st.row_name, st.number FROM booking_seats st
		JOIN booking_inventories i ON i.seat_id = st.id
		WHERE st.tier_id = ? AND st.kind = 'sellable' AND i.booked + i.held + i.blocked < i.capacity
		ORDER BY st.row_name, st.number`, tierID).Scan(&free).Error; err != nil {
		return nil, err
	}
	if len(free) == 0 {
		return nil, nil
	}
	start := s.rng.IntN(len(free))
	out := []uint{free[start].ID}
	for i := start + 1; i < len(free) && len(out) < n; i++ {
		prev := free[i-1]
		if free[i].RowName != prev.RowName || free[i].Number != prev.Number+1 {
			break
		}
		out = append(out, free[i].ID)
	}
	return out, nil
}

// cancelOne cancels one random paid simulated order on a showing, through the
// same cancel path a visitor's my-tickets panel uses.
func (s *Simulator) cancelOne(db *gorm.DB, eventID uint, now time.Time) (bool, error) {
	var orders []models.BookingOrder
	if err := db.Raw(`
		SELECT DISTINCT o.* FROM booking_orders o
		JOIN booking_order_items oi ON oi.order_id = o.id
		JOIN booking_inventories i ON i.id = oi.inventory_id
		LEFT JOIN booking_seats st ON st.id = i.seat_id
		JOIN booking_ticket_tiers t ON t.id = COALESCE(i.tier_id, st.tier_id)
		WHERE o.domain = 'event' AND o.status = 'paid' AND o.owner_ref LIKE ? AND t.event_id = ?`,
		OwnerPrefix+"%", eventID).Scan(&orders).Error; err != nil {
		return false, err
	}
	if len(orders) == 0 {
		return false, nil
	}
	o := orders[s.rng.IntN(len(orders))]
	err := ticketing.CancelOrder(db, o.ID, o.OwnerRef, now)
	if errors.Is(err, ticketing.ErrTooLate) {
		return false, nil
	}
	return err == nil, err
}

func (s *Simulator) schedule(db *gorm.DB, sched *models.BookingSchedule, now time.Time, rep *Report) error {
	horizon := min(s.cfg.Horizon, time.Duration(sched.WindowDays)*24*time.Hour)

	if s.rng.Float64() < s.cfg.CancelRate {
		var appts []models.BookingAppointment
		if err := db.Where("schedule_id = ? AND status = 'confirmed' AND guest_email LIKE ? AND start_at > ?",
			sched.ID, OwnerPrefix+"%", now).Find(&appts).Error; err != nil {
			return err
		}
		if len(appts) > 0 {
			a := appts[s.rng.IntN(len(appts))]
			if err := appointments.CancelByToken(db, a.CancelToken, now); err != nil {
				return err
			}
			rep.AppointmentsDropped++
		}
	}

	open, err := appointments.OpenSlotsFor(db, sched, now, now.Add(horizon), now)
	if err != nil || len(open) == 0 || sched.Capacity < 1 {
		return err
	}
	for i := 0; i < s.cfg.SlotsPerSchedule; i++ {
		slot := open[s.rng.IntN(len(open))]
		taken := float64(sched.Capacity-slot.Remaining) / float64(sched.Capacity)
		if taken >= s.desired(slot.StartAt.Sub(now)) {
			continue
		}
		_, err := appointments.Book(db, appointments.BookInput{
			Schedule: sched, StartAt: slot.StartAt,
			GuestName: "Aurenori guest", GuestEmail: s.owner(now),
		}, now)
		if errors.Is(err, booking.ErrSoldOut) || errors.Is(err, appointments.ErrNotOpen) || errors.Is(err, appointments.ErrPast) {
			continue
		}
		if err != nil {
			return err
		}
		rep.AppointmentsBooked++
	}
	return nil
}

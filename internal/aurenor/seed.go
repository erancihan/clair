package aurenor

import (
	"fmt"
	"time"

	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/ticketing"
	"gorm.io/gorm"
)

// Report is the per-kind tally a seed run produces, for logging.
type Report struct {
	HostCreated      bool
	SchedulesCreated int
	SchedulesSkipped int
	RulesCreated     int
	EventsCreated    int
	EventsSkipped    int
}

// Seed writes the Aurenor catalog into db idempotently: a host user, 16 free
// appointment schedules with their weekly rules, and the theater showings of the
// next WindowDays days with full seat/GA inventory. Re-running changes no row counts. It never materializes
// appointment slots — those are created lazily on first booking.
func Seed(db *gorm.DB, now time.Time, hostEmail string) (Report, error) {
	var rep Report
	if hostEmail == "" {
		hostEmail = DefaultHostEmail
	}

	// Host user — every schedule and event needs a non-zero owner.
	var host models.User
	res := db.Where(models.User{Email: hostEmail}).
		Attrs(models.User{Username: "Aurenor Host", Role: "user"}).
		FirstOrCreate(&host)
	if res.Error != nil {
		return rep, fmt.Errorf("seed host: %w", res.Error)
	}
	rep.HostCreated = res.RowsAffected == 1

	// Appointment schedules — write models directly (not the HTTP createSchedule,
	// which binds the owner to the caller's session). Rules only on first create.
	for _, def := range AllSchedules() {
		if err := db.Transaction(func(tx *gorm.DB) error {
			var sched models.BookingSchedule
			r := tx.Where(models.BookingSchedule{Slug: def.Slug}).
				Attrs(models.BookingSchedule{
					OwnerID: host.ID, Name: def.Name, Description: def.Description,
					Timezone: TZ, SlotDurationMin: def.SlotDurationMin, Capacity: def.Capacity,
					LeadTimeMin: def.LeadTimeMin, WindowDays: def.WindowDays,
					Active: true, RequiresPayment: false, Currency: Currency,
				}).FirstOrCreate(&sched)
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected != 1 {
				rep.SchedulesSkipped++
				return nil
			}
			rep.SchedulesCreated++
			for _, rl := range def.Rules {
				rule := models.BookingAvailabilityRule{
					ScheduleID: sched.ID, Weekday: rl.Weekday, StartMin: rl.StartMin, EndMin: rl.EndMin,
				}
				if err := tx.Create(&rule).Error; err != nil {
					return err
				}
				rep.RulesCreated++
			}
			return nil
		}); err != nil {
			return rep, fmt.Errorf("seed schedule %s: %w", def.Slug, err)
		}
	}

	created, skipped, err := seedEvents(db, host.ID, now)
	rep.EventsCreated, rep.EventsSkipped = created, skipped
	if err != nil {
		return rep, err
	}

	return rep, nil
}

// ExtendCalendar creates every showing in the window that does not exist yet.
// The scheduler runs it so the theater board never runs dry: without it the
// seeded showings all lie in the past a fortnight after a seed.
//
// The new showings belong to whoever owns the seeded schedules. A database that
// was never seeded has no such owner, and nothing is created.
func ExtendCalendar(db *gorm.DB, now time.Time) (int, error) {
	var sched models.BookingSchedule
	res := db.Where("slug = ?", AllSchedules()[0].Slug).Limit(1).Find(&sched)
	if res.Error != nil {
		return 0, fmt.Errorf("find calendar owner: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return 0, nil
	}
	created, _, err := seedEvents(db, sched.OwnerID, now)
	return created, err
}

// seedEvents creates the window's showings for ownerID. CreateEvent is not
// idempotent (plain insert, unique slug), so each slug is checked first and
// an existing one is skipped.
func seedEvents(db *gorm.DB, ownerID uint, now time.Time) (created, skipped int, err error) {
	for _, def := range AllEvents(now) {
		var n int64
		if err := db.Model(&models.BookingEvent{}).Where("slug = ?", def.Slug).Count(&n).Error; err != nil {
			return created, skipped, fmt.Errorf("check event %s: %w", def.Slug, err)
		}
		if n > 0 {
			skipped++
			continue
		}

		tier := ticketing.TierInput{
			Name: def.Tier.Name, Kind: def.Tier.Kind, PriceCents: def.Tier.PriceCents, Currency: Currency,
		}
		switch def.Tier.Kind {
		case "ga":
			tier.Capacity = def.Tier.Capacity
		case "seated":
			for row := 0; row < def.Tier.Rows; row++ {
				rowName := string(rune('A' + row))
				for col := 0; col < def.Tier.Cols; col++ {
					tier.Seats = append(tier.Seats, ticketing.SeatInput{
						Label:   fmt.Sprintf("%s-%d", rowName, col+1),
						Section: def.Tier.Section, RowName: rowName, Number: col + 1,
					})
				}
			}
		default:
			return created, skipped, fmt.Errorf("event %s: invalid tier kind %q", def.Slug, def.Tier.Kind)
		}

		if _, err := ticketing.CreateEvent(db, ticketing.EventInput{
			OwnerID: ownerID, Slug: def.Slug, Name: def.Name, StartAt: def.StartAt,
			Timezone: TZ, MaxPerBuyer: 6, Tiers: []ticketing.TierInput{tier},
		}); err != nil {
			return created, skipped, fmt.Errorf("create event %s: %w", def.Slug, err)
		}
		created++
	}
	return created, skipped, nil
}

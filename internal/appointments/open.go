package appointments

import (
	"time"

	"github.com/erancihan/clair/internal/database/models"
	"gorm.io/gorm"
)

// OpenSlotsFor loads a schedule's rules, exceptions and materialised slot
// counts and returns its open slots in [from, to). This is the one place that
// knows how availability is assembled from rows; the HTTP layer and, later, the
// simulator both call it rather than repeating the queries.
func OpenSlotsFor(db *gorm.DB, sched *models.BookingSchedule, from, to, now time.Time) ([]OpenSlot, error) {
	var rules []models.BookingAvailabilityRule
	if err := db.Where("schedule_id = ?", sched.ID).Find(&rules).Error; err != nil {
		return nil, err
	}
	var exceptions []models.BookingAvailabilityException
	if err := db.Where("schedule_id = ?", sched.ID).Find(&exceptions).Error; err != nil {
		return nil, err
	}

	// Only slots somebody has booked exist as rows. The join is left, not inner:
	// a slot row exists from the moment it is first booked, but its inventory
	// row is what carries the count.
	type row struct {
		StartAt  time.Time
		Booked   int
		Capacity int
	}
	var rows []row
	if err := db.Raw(`SELECT bs.start_at AS start_at, COALESCE(bi.booked, 0) AS booked, bs.capacity AS capacity
		FROM booking_slots bs
		LEFT JOIN booking_inventories bi ON bi.slot_id = bs.id
		WHERE bs.schedule_id = ? AND bs.start_at >= ? AND bs.start_at < ?`,
		sched.ID, from.UTC(), to.UTC()).Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[time.Time]SlotCount, len(rows))
	for _, r := range rows {
		counts[r.StartAt.UTC()] = SlotCount{Booked: r.Booked, Capacity: r.Capacity}
	}

	return ComputeAvailability(sched, rules, exceptions, counts, from, to, now)
}

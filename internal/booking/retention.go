package booking

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// DefaultRetention is how long booking history is kept after the business time
// it is about: a showing's start, a slot's end.
const DefaultRetention = 90 * 24 * time.Hour

// purgeBatch is how many slots one purge transaction takes on. Events go one per
// transaction, because one event can own a hundred seat inventories.
const purgeBatch = 500

// maxPurgeBatches bounds one run, so a first run over a long history spreads
// across days instead of holding the scheduler for an hour.
const maxPurgeBatches = 200

// PurgeReport counts what one purge run deleted.
type PurgeReport struct {
	Events, Slots                       int // business objects whose history was cleared
	Orders, Items, Payments             int
	Tickets, Appointments               int
	Holds, WaitlistEntries, Inventories int
}

// PurgeOldBookingData deletes booking history older than DefaultRetention.
// It is PurgeBefore with the cutoff taken from now.
func PurgeOldBookingData(db *gorm.DB, now time.Time) (PurgeReport, error) {
	return PurgeBefore(db, now.Add(-DefaultRetention), now)
}

// PurgeBefore deletes the transactional rows of every showing that started, and
// every appointment slot that ended, before cutoff.
//
// It goes by business time, never by when a row was written: a ticket bought a
// year ahead is not history until the showing has happened.
//
// What it deletes: orders, their items and payments, tickets, appointments,
// holds and waitlist entries. For a slot it also deletes the slot and its
// inventory, because both are materialized on first booking and a past slot is
// never offered again.
//
// What it never deletes: schedules, availability rules and exceptions, events,
// tiers and seats, and the inventory rows behind tiers and seats. Those are the
// catalog the pages are drawn from, not history.
//
// Money rows are status-guarded. An object with a pending order on it is
// skipped whole, because a payment for it may still be in flight; it is taken
// on a later run once the order has settled or the reaper has expired it.
// Likewise anything with a live hold.
func PurgeBefore(db *gorm.DB, cutoff, now time.Time) (PurgeReport, error) {
	var rep PurgeReport
	for batch := 0; batch < maxPurgeBatches; batch++ {
		n, err := purgeSlotBatch(db, cutoff, now, &rep)
		if err != nil {
			return rep, fmt.Errorf("purge slots: %w", err)
		}
		if n < purgeBatch {
			break
		}
	}
	for batch := 0; batch < maxPurgeBatches; batch++ {
		done, err := purgeOneEvent(db, cutoff, now, &rep)
		if err != nil {
			return rep, fmt.Errorf("purge events: %w", err)
		}
		if done {
			break
		}
	}
	return rep, nil
}

// unsettled is the guard every purge query carries: no pending order and no
// live hold on any of the inventory rows in question. %s is a subquery that
// yields those inventory ids.
const unsettled = `
	EXISTS (SELECT 1 FROM booking_order_items oi JOIN booking_orders o ON o.id = oi.order_id
	        WHERE oi.inventory_id IN (%[1]s) AND o.status = 'pending')
	OR EXISTS (SELECT 1 FROM booking_holds h
	        WHERE h.inventory_id IN (%[1]s) AND h.status = 'active' AND h.expires_at > @now)`

// purgeSlotBatch clears up to purgeBatch past slots in one transaction and
// reports how many it took.
func purgeSlotBatch(db *gorm.DB, cutoff, now time.Time, rep *PurgeReport) (int, error) {
	var slotIDs []uint
	err := WithWriteRetry(func() error {
		slotIDs = nil
		return db.Transaction(func(tx *gorm.DB) error {
			invs := `SELECT i.id FROM booking_inventories i WHERE i.slot_id = s.id`
			if err := tx.Raw(`
				SELECT s.id FROM booking_slots s
				WHERE s.end_at < @cutoff AND NOT (`+fmt.Sprintf(unsettled, invs)+`)
				ORDER BY s.id LIMIT @limit
				FOR UPDATE SKIP LOCKED`,
				map[string]any{"cutoff": cutoff, "now": now, "limit": purgeBatch}).
				Scan(&slotIDs).Error; err != nil {
				return err
			}
			if len(slotIDs) == 0 {
				return nil
			}

			var invIDs []uint
			if err := tx.Raw(`SELECT id FROM booking_inventories WHERE slot_id IN ?`, slotIDs).
				Scan(&invIDs).Error; err != nil {
				return err
			}

			var local PurgeReport
			if err := purgeInventoriesTx(tx, invIDs, &local); err != nil {
				return err
			}
			res := tx.Exec(`DELETE FROM booking_appointments WHERE slot_id IN ?`, slotIDs)
			if res.Error != nil {
				return res.Error
			}
			local.Appointments = int(res.RowsAffected)
			if len(invIDs) > 0 {
				res = tx.Exec(`DELETE FROM booking_inventories WHERE id IN ?`, invIDs)
				if res.Error != nil {
					return res.Error
				}
				local.Inventories = int(res.RowsAffected)
			}
			res = tx.Exec(`DELETE FROM booking_slots WHERE id IN ?`, slotIDs)
			if res.Error != nil {
				return res.Error
			}
			local.Slots = int(res.RowsAffected)

			rep.add(local)
			return nil
		})
	})
	return len(slotIDs), err
}

// purgeOneEvent clears one past showing's history. It reports done when no
// showing is left to clear.
func purgeOneEvent(db *gorm.DB, cutoff, now time.Time, rep *PurgeReport) (bool, error) {
	done := false
	err := WithWriteRetry(func() error {
		done = false
		return db.Transaction(func(tx *gorm.DB) error {
			invs := `SELECT i.id FROM booking_inventories i
				LEFT JOIN booking_seats st ON st.id = i.seat_id
				JOIN booking_ticket_tiers t ON t.id = COALESCE(i.tier_id, st.tier_id)
				WHERE t.event_id = e.id`
			var eventIDs []uint
			if err := tx.Raw(`
				SELECT e.id FROM booking_events e
				WHERE e.start_at < @cutoff
				  AND (EXISTS (SELECT 1 FROM booking_order_items oi WHERE oi.inventory_id IN (`+invs+`))
				    OR EXISTS (SELECT 1 FROM booking_holds h WHERE h.inventory_id IN (`+invs+`))
				    OR EXISTS (SELECT 1 FROM booking_waitlist_entries w WHERE w.inventory_id IN (`+invs+`)))
				  AND NOT (`+fmt.Sprintf(unsettled, invs)+`)
				ORDER BY e.start_at LIMIT 1
				FOR UPDATE SKIP LOCKED`,
				map[string]any{"cutoff": cutoff, "now": now}).
				Scan(&eventIDs).Error; err != nil {
				return err
			}
			if len(eventIDs) == 0 {
				done = true
				return nil
			}

			var invIDs []uint
			if err := tx.Raw(`
				SELECT i.id FROM booking_inventories i
				LEFT JOIN booking_seats st ON st.id = i.seat_id
				JOIN booking_ticket_tiers t ON t.id = COALESCE(i.tier_id, st.tier_id)
				WHERE t.event_id = ?`, eventIDs[0]).Scan(&invIDs).Error; err != nil {
				return err
			}

			var local PurgeReport
			if err := purgeInventoriesTx(tx, invIDs, &local); err != nil {
				return err
			}
			local.Events = 1
			rep.add(local)
			return nil
		})
	})
	return done, err
}

// purgeInventoriesTx deletes the history hanging off a set of inventory rows:
// the orders with items on them (and those orders' items, payments and
// tickets), then holds and waitlist entries. The caller has already checked
// that none of it is unsettled.
func purgeInventoriesTx(tx *gorm.DB, invIDs []uint, rep *PurgeReport) error {
	if len(invIDs) == 0 {
		return nil
	}

	// Pending orders are excluded here as well as by the caller's guard: a
	// money row that might still be paid for is never deleted.
	var orderIDs []uint
	if err := tx.Raw(`
		SELECT DISTINCT oi.order_id FROM booking_order_items oi
		JOIN booking_orders o ON o.id = oi.order_id
		WHERE oi.inventory_id IN ? AND o.status <> 'pending'`, invIDs).
		Scan(&orderIDs).Error; err != nil {
		return err
	}

	steps := []struct {
		count *int
		sql   string
		arg   any
		skip  bool
	}{
		{&rep.Tickets, `DELETE FROM booking_tickets WHERE order_item_id IN (SELECT id FROM booking_order_items WHERE order_id IN ?)`, orderIDs, len(orderIDs) == 0},
		{&rep.Payments, `DELETE FROM booking_payments WHERE order_id IN ?`, orderIDs, len(orderIDs) == 0},
		{&rep.Items, `DELETE FROM booking_order_items WHERE order_id IN ?`, orderIDs, len(orderIDs) == 0},
		{&rep.Orders, `DELETE FROM booking_orders WHERE id IN ?`, orderIDs, len(orderIDs) == 0},
		{&rep.Holds, `DELETE FROM booking_holds WHERE inventory_id IN ?`, invIDs, false},
		{&rep.WaitlistEntries, `DELETE FROM booking_waitlist_entries WHERE inventory_id IN ?`, invIDs, false},
	}
	for _, s := range steps {
		if s.skip {
			continue
		}
		res := tx.Exec(s.sql, s.arg)
		if res.Error != nil {
			return res.Error
		}
		*s.count += int(res.RowsAffected)
	}
	return nil
}

func (r *PurgeReport) add(o PurgeReport) {
	r.Events += o.Events
	r.Slots += o.Slots
	r.Orders += o.Orders
	r.Items += o.Items
	r.Payments += o.Payments
	r.Tickets += o.Tickets
	r.Appointments += o.Appointments
	r.Holds += o.Holds
	r.WaitlistEntries += o.WaitlistEntries
	r.Inventories += o.Inventories
}

// Total is every row the run deleted.
func (r PurgeReport) Total() int {
	return r.Orders + r.Items + r.Payments + r.Tickets + r.Appointments +
		r.Holds + r.WaitlistEntries + r.Inventories + r.Slots
}

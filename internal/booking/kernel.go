// Package booking is the shared reservation kernel: it owns capacity counting
// for every bookable thing, independent of net/http. Domain modules
// (appointments, ticketing) compose these primitives.
//
// This file contains the SQLite-and-Postgres-safe primitives used by the free
// appointment flow (EnsureInventory, BookDirect, Cancel, Block, SetCapacity).
// Holds, orders, payments, and seat groups are Postgres-only and live in the
// later phase described in docs/booking-system-design.md.
package booking

import (
	"errors"
	"time"

	"github.com/erancihan/clair/internal/database/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrSoldOut means the atomic capacity guard rejected the reservation.
	ErrSoldOut = errors.New("booking: sold out")
	// ErrCapacityBelow means a capacity reduction would drop below what is
	// already committed/held/blocked.
	ErrCapacityBelow = errors.New("booking: cannot reduce capacity below booked+held+blocked")
)

// OwnerRef identifies which typed owner a BookingInventory row belongs to.
// Using a small typed constructor (SlotOwner/TierOwner/SeatOwner) keeps the
// "exactly one owner" rule in one place.
type OwnerRef struct {
	column string
	id     uint
}

// SlotOwner references an appointment slot.
func SlotOwner(id uint) OwnerRef { return OwnerRef{column: "slot_id", id: id} }

// TierOwner references a GA ticket tier.
func TierOwner(id uint) OwnerRef { return OwnerRef{column: "tier_id", id: id} }

// SeatOwner references a single assigned seat.
func SeatOwner(id uint) OwnerRef { return OwnerRef{column: "seat_id", id: id} }

func (o OwnerRef) apply(inv *models.BookingInventory) {
	switch o.column {
	case "slot_id":
		inv.SlotID = &o.id
	case "tier_id":
		inv.TierID = &o.id
	case "seat_id":
		inv.SeatID = &o.id
	}
}

// EnsureInventory lazily materializes the inventory row for a typed owner and
// returns the canonical row (created-or-existing). It upserts then re-selects by
// the unique owner column, so the returned row always carries its ID regardless
// of whether this call created it or lost the race to a concurrent caller.
func EnsureInventory(tx *gorm.DB, owner OwnerRef, capacity int) (*models.BookingInventory, error) {
	inv := models.BookingInventory{Capacity: capacity}
	owner.apply(&inv)

	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&inv).Error; err != nil {
		return nil, err
	}
	var out models.BookingInventory
	if err := tx.Where(map[string]any{owner.column: owner.id}).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

// InventoryForSlot loads the inventory row owned by a slot (used on cancel).
func InventoryForSlot(tx *gorm.DB, slotID uint) (*models.BookingInventory, error) {
	return InventoryByOwner(tx, SlotOwner(slotID))
}

// InventoryByOwner loads the (already-materialized) inventory row for a typed
// owner. Use for owners created eagerly (ticket tiers, seats).
func InventoryByOwner(tx *gorm.DB, owner OwnerRef) (*models.BookingInventory, error) {
	var inv models.BookingInventory
	if err := tx.Where(map[string]any{owner.column: owner.id}).First(&inv).Error; err != nil {
		return nil, err
	}
	return &inv, nil
}

// BookDirect atomically reserves qty units in a single conditional UPDATE.
// RowsAffected == 0 is the sold-out / lost-race signal. This is race-free on
// Postgres (row lock re-evaluates the guard) and on SQLite (serialized writer).
func BookDirect(tx *gorm.DB, invID uint, qty int) error {
	res := tx.Model(&models.BookingInventory{}).
		Where("id = ? AND booked + held + blocked + ? <= capacity", invID, qty).
		UpdateColumn("booked", gorm.Expr("booked + ?", qty))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrSoldOut
	}
	return nil
}

// Cancel releases qty previously-booked units, then offers the freed unit(s) to
// the waitlist head (else they return to open inventory). Guarded so booked
// never goes negative; a no-op if there is nothing to release.
func Cancel(tx *gorm.DB, invID uint, qty int, now time.Time) error {
	res := tx.Model(&models.BookingInventory{}).
		Where("id = ? AND booked >= ?", invID, qty).
		UpdateColumn("booked", gorm.Expr("booked - ?", qty))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil
	}
	return offerToWaitlist(tx, invID, qty, defaultOfferTTL, now)
}

// Block removes qty sellable units without deleting the row (house/ADA/comp).
func Block(tx *gorm.DB, invID uint, qty int) error {
	return tx.Model(&models.BookingInventory{}).
		Where("id = ? AND booked + held + blocked + ? <= capacity", invID, qty).
		UpdateColumn("blocked", gorm.Expr("blocked + ?", qty)).Error
}

// SetCapacity changes capacity, refusing to drop below booked+held+blocked.
func SetCapacity(tx *gorm.DB, invID uint, newCap int) error {
	res := tx.Model(&models.BookingInventory{}).
		Where("id = ? AND ? >= booked + held + blocked", invID, newCap).
		Update("capacity", newCap)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrCapacityBelow
	}
	return nil
}

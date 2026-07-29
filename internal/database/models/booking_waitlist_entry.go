package models

import "time"

// BookingWaitlistEntry is a FIFO waitlist position on one inventory row. When a
// unit frees up, through a cancellation or an expired hold, it is offered to the
// oldest waiting entry as a time-boxed waitlist_offer hold rather than being
// returned to open inventory, so the queue is not overtaken by whoever happens
// to be refreshing the page.
//
// uq_waiting_entry keeps one waiting position per owner, so refreshing the page
// does not buy a second place in the queue. It is partial for the same reason the
// hold index is: entries that have been offered, converted or expired stay as
// history, and a plain unique index would bar the owner from ever queueing again.
type BookingWaitlistEntry struct {
	ID        uint      `json:"id" gorm:"primaryKey"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	InventoryID uint       `json:"inventory_id" gorm:"index;not null;uniqueIndex:uq_waiting_entry,priority:1,where:status = 'waiting'"`
	OwnerRef    string     `json:"-" gorm:"index;not null;uniqueIndex:uq_waiting_entry,priority:2"` // "user:<id>" | "guest:<sid>"
	Status      string     `json:"status" gorm:"index;not null;default:waiting"`                    // waiting|offered|converted|expired
	OfferToken  *string    `json:"offer_token" gorm:"index"`                                        // the waitlist_offer hold, when offered
	OfferExpiry *time.Time `json:"offer_expiry"`
}

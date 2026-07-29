// Package ticketing is the ticketing domain: events with GA pools and assigned
// seats, over the shared booking reservation kernel. It never imports net/http.
package ticketing

import (
	"errors"
	"time"

	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database/models"
	"gorm.io/gorm"
)

var (
	// ErrInvalidSeat means a requested seat is missing or not sellable.
	ErrInvalidSeat = errors.New("ticketing: invalid or non-sellable seat")
	// ErrInvalidTier means a tier kind is not ga|seated.
	ErrInvalidTier = errors.New("ticketing: tier kind must be ga or seated")
)

// SeatInput describes a seat to create under a seated tier.
type SeatInput struct {
	Label, Section, RowName string
	Number                  int
}

// TierInput describes a tier to create under an event.
type TierInput struct {
	Name       string
	Kind       string // "ga" | "seated"
	PriceCents int64
	Currency   string
	Capacity   int         // GA pool size (kind=ga)
	Seats      []SeatInput // seats (kind=seated)
}

// EventInput describes a new event and its tiers.
type EventInput struct {
	OwnerID     uint
	Slug, Name  string
	StartAt     time.Time
	Timezone    string
	MaxPerBuyer int
	Tiers       []TierInput
}

// CreateEvent creates an event with its tiers, eagerly materializing a pooled
// inventory per GA tier and a capacity-1 inventory per seat.
func CreateEvent(db *gorm.DB, in EventInput) (*models.BookingEvent, error) {
	tz := in.Timezone
	if tz == "" {
		tz = "UTC"
	}
	ev := &models.BookingEvent{
		OwnerID: in.OwnerID, Slug: in.Slug, Name: in.Name, StartAt: in.StartAt.UTC(),
		Timezone: tz, Status: "scheduled", MaxPerBuyer: in.MaxPerBuyer,
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(ev).Error; err != nil {
			return err
		}
		for _, ti := range in.Tiers {
			cur := ti.Currency
			if cur == "" {
				cur = "USD"
			}
			tier := models.BookingTicketTier{
				EventID: ev.ID, Name: ti.Name, Kind: ti.Kind, PriceCents: ti.PriceCents, Currency: cur,
			}
			if err := tx.Create(&tier).Error; err != nil {
				return err
			}
			switch ti.Kind {
			case "ga":
				if _, err := booking.EnsureInventory(tx, booking.TierOwner(tier.ID), ti.Capacity); err != nil {
					return err
				}
			case "seated":
				for _, si := range ti.Seats {
					seat := models.BookingSeat{
						TierID: tier.ID, Label: si.Label, Section: si.Section,
						RowName: si.RowName, Number: si.Number, Kind: "sellable",
					}
					if err := tx.Create(&seat).Error; err != nil {
						return err
					}
					if _, err := booking.EnsureInventory(tx, booking.SeatOwner(seat.ID), 1); err != nil {
						return err
					}
				}
			default:
				return ErrInvalidTier
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ev, nil
}

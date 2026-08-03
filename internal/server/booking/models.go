package booking

import "github.com/erancihan/clair/internal/database/models"

// Models returns the GORM models the booking domain owns, for the migration set
// composed in internal/database.
//
// The list is grouped the way the domain is layered. The reservation kernel's
// tables come first: they are the ones every selling flow shares, and neither
// appointments nor ticketing owns a counter of its own. The two front-end
// domains follow, each mapping its own vocabulary onto that kernel.
func Models() []any {
	return []any{
		// Reservation kernel: the counter, the holds over it, and the money.
		&models.BookingInventory{},
		&models.BookingHold{},
		&models.BookingOrder{},
		&models.BookingOrderItem{},
		&models.BookingPayment{},
		&models.BookingWaitlistEntry{},

		// Appointments: schedules, the rules that shape them, and the bookings.
		&models.BookingSchedule{},
		&models.BookingAvailabilityRule{},
		&models.BookingAvailabilityException{},
		&models.BookingSlot{},
		&models.BookingAppointment{},

		// Ticketing: events, their tiers and seats, and the issued tickets.
		&models.BookingEvent{},
		&models.BookingTicketTier{},
		&models.BookingSeat{},
		&models.BookingTicket{},
	}
}

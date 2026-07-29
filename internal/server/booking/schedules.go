package booking

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/erancihan/clair/internal/database/models"
	api_auth "github.com/erancihan/clair/internal/server/authentication"
	server_context "github.com/erancihan/clair/internal/server/context"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// loadSchedule fetches a schedule by its public slug, with no ownership check.
// It backs the routes anyone may call.
func loadSchedule(ctx server_context.BackEndContext, r *http.Request, slug string) (models.BookingSchedule, bool) {
	var sched models.BookingSchedule
	res := db(ctx, r).Where("slug = ?", slug).Limit(1).Find(&sched)
	return sched, res.RowsAffected == 1
}

// loadOwnedSchedule fetches a schedule the authenticated caller owns.
//
// A schedule somebody else owns reports "not found" rather than "forbidden": the
// slug is guessable, and answering "forbidden" would confirm which slugs exist
// to anyone willing to enumerate them.
func loadOwnedSchedule(ctx server_context.BackEndContext, r *http.Request, slug string) (models.BookingSchedule, bool) {
	identity, ok := api_auth.CurrentUser(r.Context())
	if !ok {
		return models.BookingSchedule{}, false
	}

	var sched models.BookingSchedule
	res := db(ctx, r).Where("slug = ? AND owner_id = ?", slug, identity.UserID).Limit(1).Find(&sched)
	return sched, res.RowsAffected == 1
}

type ruleReq struct {
	Weekday  int `json:"weekday"`
	StartMin int `json:"start_min"`
	EndMin   int `json:"end_min"`
}

type createScheduleReq struct {
	Slug            string    `json:"slug"`
	Name            string    `json:"name"`
	Description     string    `json:"description"`
	Timezone        string    `json:"timezone"`
	SlotDurationMin int       `json:"slot_duration_min"`
	Capacity        int       `json:"capacity"`
	LeadTimeMin     int       `json:"lead_time_min"`
	WindowDays      int       `json:"window_days"`
	RequiresPayment bool      `json:"requires_payment"`
	PriceCents      int64     `json:"price_cents"`
	Currency        string    `json:"currency"`
	HoldTTLMin      int       `json:"hold_ttl_min"`
	Rules           []ruleReq `json:"rules"`
}

// validRule reports whether a weekly availability window is expressible: a real
// weekday, inside a day, and ending after it starts.
func validRule(rl ruleReq) bool {
	return rl.Weekday >= 0 && rl.Weekday <= 6 &&
		rl.StartMin >= 0 && rl.EndMin <= 24*60 && rl.StartMin < rl.EndMin
}

// createSchedule creates a schedule owned by the authenticated caller, together
// with its initial availability rules.
//
// The schedule and its rules are written in one transaction. A schedule that
// committed without them would be live and permanently unbookable, since
// availability is computed from the rules.
func createSchedule(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := api_auth.CurrentUser(r.Context())
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		var req createScheduleReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request payload", http.StatusBadRequest)
			return
		}
		if req.Slug == "" || req.Name == "" {
			http.Error(w, "slug and name are required", http.StatusBadRequest)
			return
		}
		if req.SlotDurationMin <= 0 || req.Capacity < 1 {
			http.Error(w, "slot_duration_min must be > 0 and capacity >= 1", http.StatusBadRequest)
			return
		}
		// The timezone is what every rule's minute offsets are interpreted in, so
		// an unloadable one makes the whole schedule uncomputable later.
		if _, err := time.LoadLocation(req.Timezone); err != nil {
			http.Error(w, "invalid IANA timezone", http.StatusBadRequest)
			return
		}
		if req.WindowDays <= 0 {
			req.WindowDays = 30
		}
		if req.RequiresPayment {
			if req.PriceCents <= 0 {
				http.Error(w, "price_cents must be > 0 for a paid schedule", http.StatusBadRequest)
				return
			}
			if req.Currency == "" {
				req.Currency = "USD"
			}
			if req.HoldTTLMin <= 0 {
				req.HoldTTLMin = 10
			}
		}
		for _, rl := range req.Rules {
			if !validRule(rl) {
				http.Error(w, "invalid availability rule", http.StatusBadRequest)
				return
			}
		}

		sched := models.BookingSchedule{
			OwnerID: identity.UserID, Slug: req.Slug, Name: req.Name, Description: req.Description,
			Timezone: req.Timezone, SlotDurationMin: req.SlotDurationMin, Capacity: req.Capacity,
			LeadTimeMin: req.LeadTimeMin, WindowDays: req.WindowDays, Active: true,
			RequiresPayment: req.RequiresPayment, PriceCents: req.PriceCents,
			Currency: req.Currency, HoldTTLMin: req.HoldTTLMin,
		}
		err := db(ctx, r).Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&sched).Error; err != nil {
				return err
			}
			for _, rl := range req.Rules {
				if err := tx.Create(&models.BookingAvailabilityRule{
					ScheduleID: sched.ID, Weekday: rl.Weekday, StartMin: rl.StartMin, EndMin: rl.EndMin,
				}).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			ctx.Logger.Error("create schedule", zap.Error(err))
			http.Error(w, "slug likely taken or database error", http.StatusConflict)
			return
		}

		writeJSON(w, http.StatusCreated, sched)
	}
}

// listSchedules returns the schedules the authenticated caller owns.
func listSchedules(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := api_auth.CurrentUser(r.Context())
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		var schedules []models.BookingSchedule
		db(ctx, r).Where("owner_id = ?", identity.UserID).Order("created_at DESC").Find(&schedules)

		writeJSON(w, http.StatusOK, schedules)
	}
}

// getSchedule returns the owner's schedule with the rules and exceptions that
// shape it, which together are everything needed to edit it.
func getSchedule(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sched, ok := loadOwnedSchedule(ctx, r, r.PathValue("slug"))
		if !ok {
			http.Error(w, "schedule not found", http.StatusNotFound)
			return
		}

		conn := db(ctx, r)
		var rules []models.BookingAvailabilityRule
		var exceptions []models.BookingAvailabilityException
		conn.Where("schedule_id = ?", sched.ID).Order("weekday, start_min").Find(&rules)
		conn.Where("schedule_id = ?", sched.ID).Order("date").Find(&exceptions)

		writeJSON(w, http.StatusOK, map[string]any{
			"schedule": sched, "rules": rules, "exceptions": exceptions,
		})
	}
}

// addRule adds a weekly availability window to the owner's schedule.
func addRule(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sched, ok := loadOwnedSchedule(ctx, r, r.PathValue("slug"))
		if !ok {
			http.Error(w, "schedule not found", http.StatusNotFound)
			return
		}

		var body ruleReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request payload", http.StatusBadRequest)
			return
		}
		if !validRule(body) {
			http.Error(w, "invalid availability rule", http.StatusBadRequest)
			return
		}

		rule := models.BookingAvailabilityRule{
			ScheduleID: sched.ID, Weekday: body.Weekday, StartMin: body.StartMin, EndMin: body.EndMin,
		}
		if err := db(ctx, r).Create(&rule).Error; err != nil {
			ctx.Logger.Error("add availability rule", zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusCreated, rule)
	}
}

// deleteRule removes an availability rule from the owner's schedule.
//
// Appointments already booked against times the rule generated are untouched: a
// materialized slot is a row of its own, and withdrawing future availability is
// not the same as cancelling what people already hold.
func deleteRule(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sched, ok := loadOwnedSchedule(ctx, r, r.PathValue("slug"))
		if !ok {
			http.Error(w, "schedule not found", http.StatusNotFound)
			return
		}
		id, ok := parseID(r, "id")
		if !ok {
			http.Error(w, "invalid rule id", http.StatusBadRequest)
			return
		}

		// The schedule id is part of the WHERE clause, so an id belonging to
		// somebody else's schedule deletes nothing rather than being trusted
		// because the caller owns some schedule.
		res := db(ctx, r).Where("id = ? AND schedule_id = ?", id, sched.ID).
			Delete(&models.BookingAvailabilityRule{})
		if res.RowsAffected == 0 {
			http.Error(w, "rule not found", http.StatusNotFound)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

type exceptionReq struct {
	Date     string `json:"date"` // "2006-01-02"
	StartMin *int   `json:"start_min"`
	EndMin   *int   `json:"end_min"`
}

// addException blocks all or part of one date on the owner's schedule.
func addException(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sched, ok := loadOwnedSchedule(ctx, r, r.PathValue("slug"))
		if !ok {
			http.Error(w, "schedule not found", http.StatusNotFound)
			return
		}

		var body exceptionReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request payload", http.StatusBadRequest)
			return
		}
		if _, err := time.Parse("2006-01-02", body.Date); err != nil {
			http.Error(w, "date must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		// One bound without the other has no meaning: a whole-day block is both
		// omitted, a partial block is both present.
		if (body.StartMin == nil) != (body.EndMin == nil) {
			http.Error(w, "start_min and end_min must both be set or both omitted", http.StatusBadRequest)
			return
		}
		if body.StartMin != nil && (*body.StartMin < 0 || *body.EndMin > 24*60 || *body.StartMin >= *body.EndMin) {
			http.Error(w, "invalid exception window", http.StatusBadRequest)
			return
		}

		exception := models.BookingAvailabilityException{
			ScheduleID: sched.ID, Date: body.Date, StartMin: body.StartMin, EndMin: body.EndMin,
		}
		if err := db(ctx, r).Create(&exception).Error; err != nil {
			ctx.Logger.Error("add availability exception", zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusCreated, exception)
	}
}

// deleteException removes a date exception from the owner's schedule.
func deleteException(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sched, ok := loadOwnedSchedule(ctx, r, r.PathValue("slug"))
		if !ok {
			http.Error(w, "schedule not found", http.StatusNotFound)
			return
		}
		id, ok := parseID(r, "id")
		if !ok {
			http.Error(w, "invalid exception id", http.StatusBadRequest)
			return
		}

		res := db(ctx, r).Where("id = ? AND schedule_id = ?", id, sched.ID).
			Delete(&models.BookingAvailabilityException{})
		if res.RowsAffected == 0 {
			http.Error(w, "exception not found", http.StatusNotFound)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

// listBookings returns the appointments booked against the owner's schedule.
func listBookings(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sched, ok := loadOwnedSchedule(ctx, r, r.PathValue("slug"))
		if !ok {
			http.Error(w, "schedule not found", http.StatusNotFound)
			return
		}

		var appointments []models.BookingAppointment
		db(ctx, r).Where("schedule_id = ?", sched.ID).Order("start_at").Find(&appointments)

		writeJSON(w, http.StatusOK, appointments)
	}
}

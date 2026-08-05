package booking

import "gorm.io/gorm"

// NotifyChannel is the Postgres channel every inventory change is announced on.
// Payloads name what changed, not how: "event:<id>" or "schedule:<id>".
const NotifyChannel = "booking_inventory"

// The DDL installs the announcement in the database rather than in Go.
//
// Seat state is written from more than one process — the server when somebody
// holds or releases, the scheduler when the reaper frees a lapsed hold, the
// simulator later — and an in-process publisher can only see its own writes.
// A trigger sees every writer, including a hand-written UPDATE, so no code path
// can forget to announce.
//
// The WHEN clause keeps it quiet: only a change to what is booked, held or
// blocked is worth telling anyone about. Postgres queues these until COMMIT, so
// a listener never learns about a change that then rolls back.
//
// It is three constants rather than one blob because the app opens gorm with
// PrepareStmt, and Postgres refuses to prepare a string that holds more than one
// command (SQLSTATE 42601). One statement per Exec is what survives a real boot.
const notifyFunctionDDL = `
CREATE OR REPLACE FUNCTION booking_notify_inventory() RETURNS trigger AS $fn$
DECLARE
	topic text;
BEGIN
	IF NEW.tier_id IS NOT NULL THEN
		SELECT 'event:' || t.event_id INTO topic
		FROM booking_ticket_tiers t WHERE t.id = NEW.tier_id;
	ELSIF NEW.seat_id IS NOT NULL THEN
		SELECT 'event:' || t.event_id INTO topic
		FROM booking_seats s
		JOIN booking_ticket_tiers t ON t.id = s.tier_id
		WHERE s.id = NEW.seat_id;
	ELSIF NEW.slot_id IS NOT NULL THEN
		SELECT 'schedule:' || sl.schedule_id INTO topic
		FROM booking_slots sl WHERE sl.id = NEW.slot_id;
	END IF;

	IF topic IS NOT NULL THEN
		PERFORM pg_notify('` + NotifyChannel + `', topic);
	END IF;
	RETURN NULL;
END;
$fn$ LANGUAGE plpgsql
`

const dropNotifyTriggerDDL = `
DROP TRIGGER IF EXISTS booking_inventory_notify ON booking_inventories
`

const createNotifyTriggerDDL = `
CREATE TRIGGER booking_inventory_notify
AFTER UPDATE ON booking_inventories
FOR EACH ROW
WHEN (
	OLD.booked  IS DISTINCT FROM NEW.booked OR
	OLD.held    IS DISTINCT FROM NEW.held   OR
	OLD.blocked IS DISTINCT FROM NEW.blocked
)
EXECUTE FUNCTION booking_notify_inventory()
`

// InstallNotifyTriggers makes inventory changes announce themselves. Every
// statement is idempotent, so it is safe to re-run; it is part of migration, not
// of boot.
func InstallNotifyTriggers(db *gorm.DB) error {
	for _, stmt := range []string{notifyFunctionDDL, dropNotifyTriggerDDL, createNotifyTriggerDDL} {
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}

	return nil
}

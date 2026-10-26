package simulator

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"
)

// ErrProductionDSN means the guard refused the database: it looks like a
// production one and nobody said otherwise.
var ErrProductionDSN = errors.New("simulator: refusing a database that looks like production")

// localHosts are the hosts a development or compose database lives on.
var localHosts = map[string]bool{
	"localhost": true, "127.0.0.1": true, "::1": true,
	"postgres": true, "db": true, // the compose service names
}

// Guard decides whether the simulator may write to the database at dsn.
//
// It refuses when APP_ENV is production, and when the DSN names a host other
// than a local or compose one. Either refusal is lifted only by
// allowProduction, which the scheduler reads from SIM_ALLOW_PRODUCTION: writing
// made-up bookings into a real database is a decision, not a default.
func Guard(dsn, appEnv string, allowProduction bool) error {
	if allowProduction {
		return nil
	}
	if strings.EqualFold(appEnv, "production") {
		return fmt.Errorf("%w: APP_ENV is production", ErrProductionDSN)
	}
	hosts := dsnHosts(dsn)
	if len(hosts) == 0 {
		return fmt.Errorf("%w: cannot tell which host the DSN names", ErrProductionDSN)
	}
	for _, host := range hosts {
		if !localHosts[host] {
			return fmt.Errorf("%w: host %q is not a local database", ErrProductionDSN, host)
		}
	}
	return nil
}

// dsnHosts reads every host a DSN can send the driver to: the URL authority
// (which may list several, comma separated) and a host query parameter, which
// overrides it, for a URL DSN; the host key for a key=value one. Every one of
// them has to be local for the guard to pass.
func dsnHosts(dsn string) []string {
	var raw []string
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return nil
		}
		raw = append(raw, strings.Split(u.Host, ",")...)
		for _, h := range u.Query()["host"] {
			raw = append(raw, strings.Split(h, ",")...)
		}
	} else {
		for _, field := range strings.Fields(dsn) {
			if k, v, ok := strings.Cut(field, "="); ok && k == "host" {
				raw = append(raw, strings.Split(strings.Trim(v, `'"`), ",")...)
			}
		}
	}

	var out []string
	for _, h := range raw {
		h = strings.TrimSpace(h)
		if host, _, err := net.SplitHostPort(h); err == nil {
			h = host
		}
		h = strings.ToLower(strings.Trim(h, "[]"))
		if h != "" {
			out = append(out, h)
		}
	}
	return out
}

// lockKey is the advisory lock that makes the simulator a single runner across
// every scheduler process pointed at one database.
const lockKey = "clair:simulator"

// Runner owns a simulator and the session-level advisory lock that lets only
// one process tick at a time. The lock lives on a dedicated connection taken
// out of the pool for as long as the runner holds it; Postgres drops it if the
// process dies.
type Runner struct {
	sim  *Simulator
	db   *gorm.DB
	conn *sql.Conn
}

// NewRunner builds a runner around a simulator.
func NewRunner(db *gorm.DB, sim *Simulator) *Runner {
	return &Runner{sim: sim, db: db}
}

// Tick takes the lock if it does not hold it yet, then runs one simulator
// tick. ran is false when another process holds the lock; that is not an
// error, it is the second scheduler standing by.
func (r *Runner) Tick(ctx context.Context, now time.Time) (rep Report, ran bool, err error) {
	if r.conn == nil {
		held, err := r.acquire(ctx)
		if err != nil || !held {
			return rep, false, err
		}
	}
	if err := r.conn.PingContext(ctx); err != nil {
		// The lock's connection is unusable. It is discarded rather than handed
		// back to the pool: a connection that still holds the lock must never
		// end up serving somebody else. Try again next tick.
		r.discard()
		return rep, false, fmt.Errorf("simulator lock connection lost: %w", err)
	}
	rep, err = r.sim.Tick(ctx, now)
	return rep, true, err
}

func (r *Runner) acquire(ctx context.Context) (bool, error) {
	sqlDB, err := r.db.DB()
	if err != nil {
		return false, err
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return false, err
	}
	var held bool
	if err := conn.QueryRowContext(ctx,
		`SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, lockKey).Scan(&held); err != nil {
		_ = conn.Close()
		return false, err
	}
	if !held {
		_ = conn.Close()
		return false, nil
	}
	r.conn = conn
	return true, nil
}

// Close releases the lock and its connection.
func (r *Runner) Close() error {
	if r.conn == nil {
		return nil
	}
	if _, err := r.conn.ExecContext(context.Background(),
		`SELECT pg_advisory_unlock(hashtextextended($1, 0))`, lockKey); err != nil {
		// Unlocking failed, so the session may still hold the lock. Closing
		// the session is what releases it for certain.
		r.discard()
		return err
	}
	err := r.conn.Close()
	r.conn = nil
	return err
}

// discard closes the lock's connection for good instead of returning it to
// the pool. Ending the session is what makes Postgres drop the lock.
func (r *Runner) discard() {
	_ = r.conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = r.conn.Close()
	r.conn = nil
}

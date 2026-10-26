package simulator

import (
	"context"
	"database/sql"
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
	host := dsnHost(dsn)
	if host == "" {
		return fmt.Errorf("%w: cannot tell which host the DSN names", ErrProductionDSN)
	}
	if !localHosts[host] {
		return fmt.Errorf("%w: host %q is not a local database", ErrProductionDSN, host)
	}
	return nil
}

// dsnHost reads the host out of a URL DSN (postgres://...) or a key=value one
// (host=... dbname=...).
func dsnHost(dsn string) string {
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return ""
		}
		return strings.ToLower(u.Hostname())
	}
	for _, field := range strings.Fields(dsn) {
		if k, v, ok := strings.Cut(field, "="); ok && k == "host" {
			v = strings.Trim(v, `'"`)
			if h, _, err := net.SplitHostPort(v); err == nil {
				v = h
			}
			return strings.ToLower(v)
		}
	}
	return ""
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
		// The connection, and with it the lock, is gone. Try again next tick.
		_ = r.conn.Close()
		r.conn = nil
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
	_, err := r.conn.ExecContext(context.Background(),
		`SELECT pg_advisory_unlock(hashtextextended($1, 0))`, lockKey)
	cerr := r.conn.Close()
	r.conn = nil
	return errors.Join(err, cerr)
}

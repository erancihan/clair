package test

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/erancihan/clair/internal/aurenor"
	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/cmd"
	"github.com/erancihan/clair/internal/server"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// TestServerCmdFailsFastOnAnUnreachableDatabase: a container with a bad
// DATABASE_URL must exit non-zero, or the orchestrator believes it is up.
func TestServerCmdFailsFastOnAnUnreachableDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://clair:clair@127.0.0.1:1/clair?sslmode=disable&connect_timeout=1")
	t.Setenv("SERVER_PORT", "5099")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := cmd.ServerCmd(ctx).ExecuteContext(ctx)
	if err == nil {
		t.Fatal("server command returned nil with an unreachable database")
	}
	if ctx.Err() != nil {
		t.Fatal("server command only failed because the test timed out")
	}
}

// TestShutdownEndsOpenStreams: an SSE handler never returns on its own, so a
// graceful shutdown that waits for handlers would wait the whole timeout and
// then cut the connections anyway. The server has to tell its streams to end.
func TestShutdownEndsOpenStreams(t *testing.T) {
	db := newAuthTestDB(t)
	// Seed one event so the stream has something to open on.
	seedOneShowing(t, db)

	backend := server.NewBackEnd(context.Background(), zap.NewNop(), nil, db)
	srv := backend.Server(0)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(listener) }()
	base := "http://" + listener.Addr().String()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/events/summary/stream?slugs=grand-dune-part-two-1000", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status %d", resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	if _, err := reader.ReadString('\n'); err != nil { // the first frame
		t.Fatal(err)
	}

	start := time.Now()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("shutdown with one open stream took %s; the stream was not told to end", took)
	}
	// The client sees its stream end.
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if !strings.HasPrefix(line, ":") && strings.TrimSpace(line) != "" {
			continue
		}
	}
}

// seedOneShowing gives db the Aurenor catalog and the notify triggers a stream
// listens on, the same two calls newTheaterServer makes.
func seedOneShowing(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := booking.InstallNotifyTriggers(db); err != nil {
		t.Fatalf("install notify triggers: %v", err)
	}
	if _, err := aurenor.Seed(db, time.Now(), "seed@aurenor.test"); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

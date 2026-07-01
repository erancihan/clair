package games

import (
	"testing"

	game "github.com/erancihan/clair/internal/games/chess"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestChessPersistenceRoundTrip(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := InitChessPersistence(db); err != nil {
		t.Fatalf("init persistence: %v", err)
	}

	g, id := game.NewGame(game.TypePvP)
	g.Join() // white
	g.Join() // black -> game starts, persisted on broadcast
	if err := g.MakeMove("white", "e2", "e4", ""); err != nil {
		t.Fatalf("e2-e4: %v", err)
	}
	if err := g.MakeMove("black", "e7", "e5", ""); err != nil {
		t.Fatalf("e7-e5: %v", err)
	}

	var rec chessGameRecord
	if err := db.First(&rec, "id = ?", id).Error; err != nil {
		t.Fatalf("game should have been persisted: %v", err)
	}
	if rec.Status != string(game.StatusOngoing) {
		t.Errorf("persisted status = %q, want Ongoing", rec.Status)
	}
	if rec.FEN == "" {
		t.Error("persisted FEN should not be empty")
	}
	if rec.MovesJSON == "" || rec.MovesJSON == "null" {
		t.Errorf("persisted moves should be present, got %q", rec.MovesJSON)
	}
}

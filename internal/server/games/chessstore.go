package games

import (
	"encoding/json"
	"time"

	game "github.com/erancihan/clair/internal/games/chess"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// chessGameRecord is the persisted form of a chess game.
type chessGameRecord struct {
	ID          string `gorm:"primaryKey"`
	GameType    int
	Status      string
	FEN         string
	WhiteTimeMs int64
	BlackTimeMs int64
	WhiteToken  string
	BlackToken  string
	MovesJSON   string
	UpdatedAt   time.Time
}

func (chessGameRecord) TableName() string { return "chess_games" }

// gormChessStore implements game.Store on top of GORM.
type gormChessStore struct {
	db *gorm.DB
}

func (s *gormChessStore) Save(snap game.Snapshot) {
	movesJSON, _ := json.Marshal(snap.Moves)
	rec := chessGameRecord{
		ID:          snap.ID,
		GameType:    snap.GameType,
		Status:      snap.Status,
		FEN:         snap.FEN,
		WhiteTimeMs: snap.WhiteTimeMs,
		BlackTimeMs: snap.BlackTimeMs,
		WhiteToken:  snap.WhiteToken,
		BlackToken:  snap.BlackToken,
		MovesJSON:   string(movesJSON),
	}
	// Upsert by primary key.
	s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		UpdateAll: true,
	}).Create(&rec)
}

func (s *gormChessStore) Delete(id string) {
	s.db.Delete(&chessGameRecord{}, "id = ?", id)
}

// InitChessPersistence migrates the chess table, installs the store, and reloads
// in-progress games into memory. It is a no-op when db is nil.
func InitChessPersistence(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if err := db.AutoMigrate(&chessGameRecord{}); err != nil {
		return err
	}
	game.SetStore(&gormChessStore{db: db})

	var records []chessGameRecord
	db.Where("status = ?", string(game.StatusOngoing)).Find(&records)
	for _, rec := range records {
		var moves []string
		if rec.MovesJSON != "" {
			_ = json.Unmarshal([]byte(rec.MovesJSON), &moves)
		}
		if _, err := game.LoadSnapshot(game.Snapshot{
			ID:          rec.ID,
			GameType:    rec.GameType,
			Status:      rec.Status,
			FEN:         rec.FEN,
			WhiteTimeMs: rec.WhiteTimeMs,
			BlackTimeMs: rec.BlackTimeMs,
			WhiteToken:  rec.WhiteToken,
			BlackToken:  rec.BlackToken,
			Moves:       moves,
		}); err != nil {
			continue // skip unparseable records rather than fail startup
		}
	}
	return nil
}

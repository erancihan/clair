package utils

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
)

// generate random 4-4 hex string ID for game instance
var charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func GenerateGameID() string {
	bytes := make([]byte, 8)

	for i := range bytes {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		bytes[i] = charset[n.Int64()]
	}

	return string(bytes[:4]) + "-" + string(bytes[4:])
}

// GenerateToken returns a random 48-character hex token, used to authorize a
// player's seat within a game.
func GenerateToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

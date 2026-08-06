package test

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

// chessClient is an HTTP client with a cookie jar, so it behaves like one
// browser: the sid cookie the identity layer sets on the first call is sent back
// on every later one.
func chessClient(t *testing.T) *http.Client {
	t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}

	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func createChessGame(t *testing.T, client *http.Client, base string) map[string]any {
	t.Helper()

	resp, err := client.Post(base+"/games/chess/create", "application/json",
		strings.NewReader(`{"game_mode":"pvp"}`))
	if err != nil {
		t.Fatalf("create game: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from create, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return body
}

func myChessGames(t *testing.T, client *http.Client, base string) map[string]any {
	t.Helper()

	resp, err := client.Get(base + "/games/chess/mine")
	if err != nil {
		t.Fatalf("list my games: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /mine, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /mine response: %v", err)
	}
	return body
}

// TestChessMineFindsAnAnonymousPlayersGame is the point of adopting OwnerRef: a
// visitor who never logged in can still find the game they are sitting in,
// because the guest cookie identifies them across requests.
func TestChessMineFindsAnAnonymousPlayersGame(t *testing.T) {
	srv := newRoutesServer(t)
	client := chessClient(t)

	created := createChessGame(t, client, srv.URL)
	gameID, _ := created["game_id"].(string)
	if gameID == "" {
		t.Fatal("create should return a game id")
	}

	body := myChessGames(t, client, srv.URL)

	// The response says out loud that this list is in-memory only, so a caller
	// cannot mistake it for durable history.
	if ephemeral, _ := body["ephemeral"].(bool); !ephemeral {
		t.Error("/mine must declare itself ephemeral")
	}

	games, _ := body["games"].([]any)
	if len(games) != 1 {
		t.Fatalf("expected exactly the one game just created, got %d", len(games))
	}

	got, _ := games[0].(map[string]any)
	if id, _ := got["game_id"].(string); id != gameID {
		t.Errorf("expected game %s, got %s", gameID, id)
	}
	if color, _ := got["color"].(string); color != "white" {
		t.Errorf("the creator takes the white seat, got %q", color)
	}
	// The seat token comes back so a player who lost their local copy can resume.
	if token, _ := got["token"].(string); token != created["token"] {
		t.Error("/mine should return the seat token that create issued")
	}
}

// TestChessMineIsScopedToTheOwner checks the attribution actually separates
// visitors: a second browser must not see the first one's game.
func TestChessMineIsScopedToTheOwner(t *testing.T) {
	srv := newRoutesServer(t)

	owner := chessClient(t)
	createChessGame(t, owner, srv.URL)

	stranger := chessClient(t)
	body := myChessGames(t, stranger, srv.URL)

	games, _ := body["games"].([]any)
	if len(games) != 0 {
		t.Errorf("a different visitor must not see someone else's game, got %+v", games)
	}
}

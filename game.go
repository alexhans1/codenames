package codenames

import (
	"bytes"
	"encoding/base64"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

const wordsPerGame = 25

type Team int

const (
	Neutral Team = iota
	Red
	Blue
	Black
)

func (t Team) String() string {
	switch t {
	case Red:
		return "red"
	case Blue:
		return "blue"
	case Black:
		return "black"
	default:
		return "neutral"
	}
}

func (t Team) Other() Team {
	if t == Red {
		return Blue
	}
	if t == Blue {
		return Red
	}
	return t
}

func (t Team) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.String())
}

func (t Team) Repeat(n int) []Team {
	s := make([]Team, n)
	for i := 0; i < n; i++ {
		s[i] = t
	}
	return s
}

// GameState encapsulates enough data to reconstruct
// a Game's state. It's used to recreate games after
// a process restart.
type GameState struct {
	Seed     int64    `json:"seed"`
	Round    int      `json:"round"`
	Revealed []bool   `json:"revealed"`
	WordSet  []string `json:"word_set"`
}

func (gs GameState) ID() string {
	var buf bytes.Buffer
	err := gob.NewEncoder(&buf).Encode(gs)
	if err != nil {
		return ""
	}
	return base64.URLEncoding.EncodeToString(buf.Bytes())
}

func (gs GameState) anyRevealed() bool {
	var revealed bool
	for _, r := range gs.Revealed {
		revealed = revealed || r
	}
	return revealed
}

func decodeGameState(s string, defaultWords []string) (GameState, bool) {
	data, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return GameState{}, false
	}
	var state GameState
	err = gob.NewDecoder(bytes.NewReader(data)).Decode(&state)
	if err != nil {
		return GameState{}, false
	}
	if len(state.WordSet) == 0 {
		state.WordSet = defaultWords
	}
	if len(state.WordSet) < 25 {
		return GameState{}, false
	}
	return state, true
}

func randomState(words []string) GameState {
	return GameState{
		Seed:     rand.Int63(),
		Revealed: make([]bool, wordsPerGame),
		WordSet:  words,
	}
}

type Game struct {
	GameState
	ID           string    `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	StartingTeam Team      `json:"starting_team"`
	WinningTeam  *Team     `json:"winning_team,omitempty"`
	Words        []string  `json:"words"`
	Layout       []Team    `json:"layout"`
	EndTime      time.Time `json:"end_time"`

	mu        sync.Mutex
	marshaled []byte
}

type noCachedMarshal Game

// MarshalJSON implements the encoding/json.Marshaler interface.
// It caches a marshalled value of the game object.
func (g *Game) MarshalJSON() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var err error
	if g.marshaled == nil {
		// Marshal g, wrapping it in the `noCachedMarshal` type
		// to erase this custom json.Marshaler implementation.
		uncached := noCachedMarshal(*g)
		g.marshaled, err = json.Marshal(uncached)
	}

	return g.marshaled, err
}

func (g *Game) checkWinningCondition() {
	if g.WinningTeam != nil {
		return
	}
	var redRemaining, blueRemaining bool
	for i, t := range g.Layout {
		if g.Revealed[i] {
			continue
		}
		switch t {
		case Red:
			redRemaining = true
		case Blue:
			blueRemaining = true
		}
	}
	if !redRemaining {
		winners := Red
		g.WinningTeam = &winners
	}
	if !blueRemaining {
		winners := Blue
		g.WinningTeam = &winners
	}
}

func (g *Game) NextTurn() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.marshaled = nil

	if g.WinningTeam != nil {
		return errors.New("game is already over")
	}
	g.Round++
	g.EndTime = getEndTime(1, 0)
	return nil
}

func (g *Game) Guess(idx int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.marshaled = nil

	if idx > len(g.Layout) || idx < 0 {
		return fmt.Errorf("index %d is invalid", idx)
	}
	if g.Revealed[idx] {
		return errors.New("cell has already been revealed")
	}
	g.Revealed[idx] = true

	if g.Layout[idx] == Black {
		winners := g.currentTeam().Other()
		g.WinningTeam = &winners
		return nil
	}

	g.checkWinningCondition()
	if g.Layout[idx] != g.currentTeam() {
		g.Round = g.Round + 1
	}
	return nil
}

func (g *Game) currentTeam() Team {
	if g.Round%2 == 0 {
		return g.StartingTeam
	}
	return g.StartingTeam.Other()
}

func getEndTime(minutes int, seconds int) time.Time {
	return time.Now().Add(time.Minute * time.Duration(minutes)).Add(time.Second * time.Duration(seconds))
}

func newGame(id string, state GameState) *Game {
	rnd := rand.New(rand.NewSource(state.Seed))
	game := &Game{
		ID:           id,
		CreatedAt:    time.Now(),
		StartingTeam: Team(rnd.Intn(2)) + Red,
		Words:        make([]string, 0, wordsPerGame),
		Layout:       make([]Team, 0, wordsPerGame),
		GameState:    state,
		EndTime:      getEndTime(1, 0),
	}

	// Pick 25 random words.
	used := map[string]struct{}{}
	for len(used) < wordsPerGame {
		w := state.WordSet[rnd.Intn(len(state.WordSet))]
		if _, ok := used[w]; !ok {
			used[w] = struct{}{}
			game.Words = append(game.Words, w)
		}
	}

	// Pick a random permutation of team assignments.
	var teamAssignments []Team
	teamAssignments = append(teamAssignments, Red.Repeat(8)...)
	teamAssignments = append(teamAssignments, Blue.Repeat(8)...)
	teamAssignments = append(teamAssignments, Neutral.Repeat(7)...)
	teamAssignments = append(teamAssignments, Black)
	teamAssignments = append(teamAssignments, game.StartingTeam)

	shuffleCount := rnd.Intn(5) + 5
	for i := 0; i < shuffleCount; i++ {
		shuffle(rnd, teamAssignments)
	}
	game.Layout = teamAssignments
	return game
}

func shuffle(rnd *rand.Rand, teamAssignments []Team) {
	for i := range teamAssignments {
		j := rnd.Intn(i + 1)
		teamAssignments[i], teamAssignments[j] = teamAssignments[j], teamAssignments[i]
	}
}

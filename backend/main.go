package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type GameState string

const (
	StateLobby  GameState = "LOBBY"
	StateSubmit GameState = "SUBMIT"
	StateVoting GameState = "VOTING"
	StateReveal GameState = "REVEAL"
	StateEnd    GameState = "END"
)

type User struct {
	ID           string
	Conn         *websocket.Conn
	Send         chan interface{}
	Score        int
	Songs        []Song
	HasSubmitted bool
}

type Song struct {
	ID      string `json:"id"`
	URI     string `json:"uri"`
	Name    string `json:"name"`
	Artist  string `json:"artist"`
	Img     string `json:"img"`
	OwnerID string `json:"owner_id"`
}

type Lobby struct {
	ID          string
	Name        string
	HostID      string
	Users       map[string]*User
	State       GameState
	AllSongs    []Song
	CurrentSong *Song
	Votes       map[string]string
	Round       int
	Mutex       sync.Mutex
	CancelTimer context.CancelFunc
}

var lobbies = make(map[string]*Lobby)
var lobbiesMutex = sync.Mutex{}

var spotifyAppToken string
var tokenExpiry time.Time
var tokenMutex sync.RWMutex

func getSecret(name string, envFallback string) string {
	data, err := os.ReadFile(fmt.Sprintf("/run/secrets/%s", name))
	if err == nil {
		return strings.TrimSpace(string(data))
	}
	return os.Getenv(envFallback)
}

func ensureSpotifyToken() error {
	tokenMutex.RLock()
	isValid := time.Now().Before(tokenExpiry) && spotifyAppToken != ""
	tokenMutex.RUnlock()
	if isValid {
		return nil
	}

	tokenMutex.Lock()
	defer tokenMutex.Unlock()
	if time.Now().Before(tokenExpiry) && spotifyAppToken != "" {
		return nil
	}

	clientID := getSecret("CLIENT_ID", "CLIENT_ID")
	clientSecret := getSecret("CLIENT_SECRET", "CLIENT_SECRET")

	req, _ := http.NewRequest("POST", "https://accounts.spotify.com/api/token", strings.NewReader("grant_type=client_credentials"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(clientID+":"+clientSecret)))

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	if token, ok := result["access_token"].(string); ok {
		spotifyAppToken = token
		expiresIn := result["expires_in"].(float64)
		tokenExpiry = time.Now().Add(time.Duration(expiresIn-60) * time.Second)
		return nil
	}
	return fmt.Errorf("failed to retrieve access_token")
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	http.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
	})
	http.HandleFunc("/api/lobbies", handleGetLobbies)
	http.HandleFunc("/api/spotify/search", handleSpotifySearch)
	http.HandleFunc("/ws", handleConnections)

	slog.Info("Go backend starting on :5000")
	http.ListenAndServe(":5000", nil)
}

func handleGetLobbies(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	lobbiesMutex.Lock()
	defer lobbiesMutex.Unlock()

	type LobbyInfo struct {
		ID          string `json:"id"`
		HostID      string `json:"host_id"`
		PlayerCount int    `json:"player_count"`
		State       string `json:"state"`
	}

	activeLobbies := make([]LobbyInfo, 0, len(lobbies))
	for id, lobby := range lobbies {
		lobby.Mutex.Lock()
		activeLobbies = append(activeLobbies, LobbyInfo{
			ID:          id,
			HostID:      lobby.HostID,
			PlayerCount: len(lobby.Users),
			State:       string(lobby.State),
		})
		lobby.Mutex.Unlock()
	}

	json.NewEncoder(w).Encode(activeLobbies)
}

func handleSpotifySearch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	query := r.URL.Query().Get("q")
	if query == "" {
		http.Error(w, "Missing search query", http.StatusBadRequest)
		return
	}

	if err := ensureSpotifyToken(); err != nil {
		http.Error(w, "Internal Spotify Auth Error", http.StatusInternalServerError)
		return
	}

	searchURL := fmt.Sprintf("https://api.spotify.com/v1/search?type=track&limit=10&q=%s", url.QueryEscape(query))
	req, _ := http.NewRequest("GET", searchURL, nil)

	tokenMutex.RLock()
	req.Header.Set("Authorization", "Bearer "+spotifyAppToken)
	tokenMutex.RUnlock()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Error contacting Spotify", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	var userID string
	var currentLobby *Lobby

	defer func() {
		if currentLobby != nil && userID != "" {
			currentLobby.Mutex.Lock()
			if user, exists := currentLobby.Users[userID]; exists && user.Conn == ws {
				close(user.Send)
				delete(currentLobby.Users, userID)
			}
			isEmpty := len(currentLobby.Users) == 0
			currentLobby.Mutex.Unlock()

			if isEmpty {
				lobbiesMutex.Lock()
				delete(lobbies, currentLobby.ID)
				lobbiesMutex.Unlock()
				if currentLobby.CancelTimer != nil {
					currentLobby.CancelTimer()
				}
			} else {
				broadcastLobbyState(currentLobby)
			}
		}
		ws.Close()
	}()

	for {
		var msg map[string]interface{}
		if err := ws.ReadJSON(&msg); err != nil {
			break
		}

		action, ok := msg["action"].(string)
		if !ok {
			continue
		}

		switch action {
		case "JOIN_LOBBY":
			lobbyID, _ := msg["lobby_id"].(string)
			userIDReq, _ := msg["username"].(string)
			if lobbyID == "" || userIDReq == "" {
				continue
			}

			userID = userIDReq

			lobbiesMutex.Lock()
			if _, exists := lobbies[lobbyID]; !exists {
				lobbies[lobbyID] = &Lobby{
					ID:     lobbyID,
					HostID: userID,
					Users:  make(map[string]*User),
					State:  StateLobby,
					Votes:  make(map[string]string),
				}
			}
			currentLobby = lobbies[lobbyID]
			lobbiesMutex.Unlock()

			currentLobby.Mutex.Lock()
			if existingUser, exists := currentLobby.Users[userID]; exists {
				close(existingUser.Send)
			}
			newUser := &User{ID: userID, Conn: ws, Send: make(chan interface{}, 256), Score: 0}
			currentLobby.Users[userID] = newUser
			go writePump(ws, newUser.Send)
			currentLobby.Mutex.Unlock()

			broadcastLobbyState(currentLobby)

		case "START_SUBMISSION":
			if currentLobby != nil && userID == currentLobby.HostID {
				currentLobby.Mutex.Lock()
				currentLobby.State = StateSubmit
				currentLobby.Mutex.Unlock()
				broadcastLobbyState(currentLobby)
			}

		case "SUBMIT_SONGS":
			songsData, ok := msg["songs"].([]interface{})
			if !ok || currentLobby == nil {
				continue
			}

			var userSongs []Song
			for _, s := range songsData {
				songMap, ok := s.(map[string]interface{})
				if !ok { continue }
				uri, _ := songMap["uri"].(string)
				parts := strings.Split(uri, ":")
				trackID := ""
				if len(parts) == 3 { trackID = parts[2] }

				userSongs = append(userSongs, Song{
					ID: trackID, URI: uri, Name: songMap["name"].(string),
					Artist: songMap["artist"].(string), Img: songMap["img"].(string),
					OwnerID: userID,
				})
			}

			currentLobby.Mutex.Lock()
			if currentLobby.State == StateSubmit {
				if user, exists := currentLobby.Users[userID]; exists {
					user.Songs = userSongs
					user.HasSubmitted = true
				}
			}
			currentLobby.Mutex.Unlock()
			broadcastLobbyState(currentLobby)

		case "START_GAME", "NEXT_SONG":
			if currentLobby != nil && userID == currentLobby.HostID {
				currentLobby.Mutex.Lock()
				if currentLobby.State == StateSubmit {
					currentLobby.AllSongs = []Song{}
					for _, u := range currentLobby.Users {
						if u.HasSubmitted {
							currentLobby.AllSongs = append(currentLobby.AllSongs, u.Songs...)
						}
					}
					rand.Shuffle(len(currentLobby.AllSongs), func(i, j int) {
						currentLobby.AllSongs[i], currentLobby.AllSongs[j] = currentLobby.AllSongs[j], currentLobby.AllSongs[i]
					})
				}

				if currentLobby.CancelTimer != nil {
					currentLobby.CancelTimer()
				}
				currentLobby.Mutex.Unlock()
				playNextSong(currentLobby)
			}

		case "CAST_VOTE":
            votedFor, _ := msg["voted_for"].(string)
            currentLobby.Mutex.Lock()
            if currentLobby.State == StateVoting {
                if currentLobby.CurrentSong != nil && currentLobby.CurrentSong.OwnerID != userID {
                    currentLobby.Votes[userID] = votedFor
                }
            }
            currentLobby.Mutex.Unlock()
            broadcastLobbyState(currentLobby)
		}
	}
}

func writePump(ws *websocket.Conn, send chan interface{}) {
	for msg := range send {
		if err := ws.WriteJSON(msg); err != nil {
			break
		}
	}
}

func playNextSong(lobby *Lobby) {
	lobby.Mutex.Lock()
	if len(lobby.AllSongs) == 0 {
		lobby.State = StateEnd
		lobby.Mutex.Unlock()
		broadcastLobbyState(lobby)
		return
	}

	lobby.CurrentSong = &lobby.AllSongs[0]
	lobby.AllSongs = lobby.AllSongs[1:]
	lobby.State = StateVoting
	lobby.Votes = make(map[string]string)
	lobby.Round++

	ctx, cancel := context.WithCancel(context.Background())
	lobby.CancelTimer = cancel
	currentRound := lobby.Round
	lobby.Mutex.Unlock()

	broadcastLobbyState(lobby)

	go func(ctx context.Context, l *Lobby, expectedRound int) {
		select {
		case <-time.After(45 * time.Second):
			l.Mutex.Lock()
			if l.State == StateVoting && l.Round == expectedRound {
				l.State = StateReveal
				for voter, votedFor := range l.Votes {
					if votedFor == l.CurrentSong.OwnerID {
						if voterUser, exists := l.Users[voter]; exists {
							voterUser.Score += 100
						}
					}
				}
				l.Mutex.Unlock()
				broadcastLobbyState(l)
			} else {
				l.Mutex.Unlock()
			}
		case <-ctx.Done():
		}
	}(ctx, lobby, currentRound)
}

func broadcastLobbyState(lobby *Lobby) {
	lobby.Mutex.Lock()
	defer lobby.Mutex.Unlock()

	players := []string{}
	submittedPlayers := make(map[string]bool)
	scores := make(map[string]int)

	for id, u := range lobby.Users {
		players = append(players, id)
		submittedPlayers[id] = u.HasSubmitted
		scores[id] = u.Score
	}

	stateMsg := map[string]interface{}{
		"type":              "STATE_UPDATE",
		"state":             lobby.State,
		"host":              lobby.HostID,
		"current_song":      lobby.CurrentSong,
		"players":           players,
		"submitted_players": submittedPlayers,
	}

	if lobby.State == StateReveal || lobby.State == StateEnd {
		stateMsg["scores"] = scores
		stateMsg["votes"] = lobby.Votes
	}

	for _, user := range lobby.Users {
        userMsg := make(map[string]interface{})
        for k, v := range stateMsg {
            userMsg[k] = v
        }
        if lobby.State == StateVoting && lobby.CurrentSong != nil {
            songCopy := *lobby.CurrentSong
            if user.ID != lobby.CurrentSong.OwnerID {
                songCopy.OwnerID = ""
            }
            userMsg["current_song"] = songCopy
        }

        select {
        case user.Send <- userMsg:
        default:
            slog.Warn("User send channel full, dropping message", "user_id", user.ID)
        }
    }
}
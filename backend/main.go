package main

import (
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
	WriteMutex   sync.Mutex
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
	HostID      string
	Users       map[string]*User
	State       GameState
	AllSongs    []Song
	CurrentSong *Song
	Votes       map[string]string
	Round       int
	Mutex       sync.Mutex
}

var lobbies = make(map[string]*Lobby)
var lobbiesMutex = sync.Mutex{}

var spotifyAppToken string
var tokenExpiry time.Time
var tokenMutex sync.Mutex

func getSecret(name string, envFallback string) string {
	data, err := os.ReadFile(fmt.Sprintf("/run/secrets/%s", name))
	if err == nil {
		return strings.TrimSpace(string(data))
	}
	return os.Getenv(envFallback)
}

func ensureSpotifyToken() error {
	tokenMutex.Lock()
	defer tokenMutex.Unlock()

	if time.Now().Before(tokenExpiry) && spotifyAppToken != "" {
		return nil
	}

	clientID := getSecret("CLIENT_ID", "CLIENT_ID")
	clientSecret := getSecret("CLIENT_SECRET", "CLIENT_SECRET")

	if clientID == "" || clientSecret == "" {
		return fmt.Errorf("spotify credentials not found in secrets or environment")
	}

	req, _ := http.NewRequest("POST", "https://accounts.spotify.com/api/token", strings.NewReader("grant_type=client_credentials"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(clientID+":"+clientSecret)))

	client := &http.Client{Timeout: 10 * time.Second}
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
		slog.Info("Successfully refreshed Spotify App Token", "expires_in_seconds", expiresIn)
		return nil
	}
	return fmt.Errorf("failed to retrieve access_token from spotify response")
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	http.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
	})

	http.HandleFunc("/api/spotify/search", handleSpotifySearch)
	http.HandleFunc("/ws", handleConnections)

	port := "5000"
	slog.Info("Go backend starting", "port", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		slog.Error("Server failed to start", "error", err)
		os.Exit(1)
	}
}

func handleSpotifySearch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	query := r.URL.Query().Get("q")
	if query == "" {
		http.Error(w, "Missing search query", http.StatusBadRequest)
		slog.Warn("Search request rejected: missing query parameter")
		return
	}

	if err := ensureSpotifyToken(); err != nil {
		slog.Error("Spotify Auth Error", "error", err)
		http.Error(w, "Internal Spotify Auth Error", http.StatusInternalServerError)
		return
	}

	searchURL := fmt.Sprintf("https://api.spotify.com/v1/search?type=track&limit=10&q=%s", url.QueryEscape(query))
	req, _ := http.NewRequest("GET", searchURL, nil)
	req.Header.Set("Authorization", "Bearer "+spotifyAppToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("Error contacting Spotify API", "error", err)
		http.Error(w, "Error contacting Spotify", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
	slog.Debug("Proxied Spotify search request successfully", "query", query)
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("WebSocket Upgrade Error", "error", err, "client_ip", r.RemoteAddr)
		return
	}

	var userID string
	var currentLobby *Lobby

	defer func() {
		if currentLobby != nil && userID != "" {
			currentLobby.Mutex.Lock()
			// Prevent rapid-refresh race conditions from deleting active reconnects
			if user, exists := currentLobby.Users[userID]; exists && user.Conn == ws {
				delete(currentLobby.Users, userID)
				slog.Info("User disconnected and removed from lobby", "user_id", userID, "lobby_id", currentLobby.ID)
			}
			isEmpty := len(currentLobby.Users) == 0
			currentLobby.Mutex.Unlock()

			if isEmpty {
				lobbiesMutex.Lock()
				delete(lobbies, currentLobby.ID)
				lobbiesMutex.Unlock()
				slog.Info("Lobby closed due to inactivity", "lobby_id", currentLobby.ID)
			} else {
				broadcastLobbyState(currentLobby)
			}
		}
		ws.Close()
	}()

	for {
		var msg map[string]interface{}
		err := ws.ReadJSON(&msg)
		if err != nil {
			slog.Debug("WebSocket read error/disconnect", "error", err, "user_id", userID)
			break
		}

		action, ok := msg["action"].(string)
		if !ok {
			continue
		}

		switch action {
		case "JOIN_LOBBY":
			lobbyID, ok1 := msg["lobby_id"].(string)
			userIDReq, ok2 := msg["username"].(string)
			if !ok1 || !ok2 || lobbyID == "" || userIDReq == "" {
				slog.Warn("Invalid JOIN_LOBBY payload", "payload", msg)
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
					Round:  0,
				}
				slog.Info("New lobby created", "lobby_id", lobbyID, "host_id", userID)
			}
			currentLobby = lobbies[lobbyID]
			lobbiesMutex.Unlock()

			currentLobby.Mutex.Lock()
			if existingUser, exists := currentLobby.Users[userID]; exists {
				existingUser.WriteMutex.Lock()
				existingUser.Conn = ws
				existingUser.WriteMutex.Unlock()
				slog.Info("User reconnected to lobby", "user_id", userID, "lobby_id", lobbyID)
			} else {
				currentLobby.Users[userID] = &User{ID: userID, Conn: ws, Score: 0, HasSubmitted: false}
				slog.Info("User joined lobby", "user_id", userID, "lobby_id", lobbyID)
			}
			currentLobby.Mutex.Unlock()
			broadcastLobbyState(currentLobby)

		case "START_SUBMISSION":
			if currentLobby != nil && userID == currentLobby.HostID {
				currentLobby.Mutex.Lock()
				currentLobby.State = StateSubmit
				for _, u := range currentLobby.Users {
					u.HasSubmitted = false
				}
				currentLobby.Mutex.Unlock()
				slog.Info("Host started submission phase", "lobby_id", currentLobby.ID)
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
				if !ok {
					continue
				}

				uri, _ := songMap["uri"].(string)
				name, _ := songMap["name"].(string)
				artist, _ := songMap["artist"].(string)
				img, _ := songMap["img"].(string)

				parts := strings.Split(uri, ":")
				trackID := ""
				if len(parts) == 3 {
					trackID = parts[2]
				}

				userSongs = append(userSongs, Song{
					ID:      trackID,
					URI:     uri,
					Name:    name,
					Artist:  artist,
					Img:     img,
					OwnerID: userID,
				})
			}

			currentLobby.Mutex.Lock()
			if currentLobby.State == StateSubmit {
				if user, exists := currentLobby.Users[userID]; exists {
					user.Songs = userSongs
					user.HasSubmitted = true
					slog.Info("User submitted songs", "user_id", userID, "lobby_id", currentLobby.ID, "song_count", len(userSongs))
				}
			}
			currentLobby.Mutex.Unlock()

			broadcastLobbyState(currentLobby)

		case "START_GAME":
			if currentLobby != nil && userID == currentLobby.HostID {
				currentLobby.Mutex.Lock()
				validTransition := false

				if currentLobby.State == StateSubmit {
					currentLobby.AllSongs = []Song{}
					for _, u := range currentLobby.Users {
						if u.HasSubmitted {
							currentLobby.AllSongs = append(currentLobby.AllSongs, u.Songs...)
						}
					}
					rand.Seed(time.Now().UnixNano())
					rand.Shuffle(len(currentLobby.AllSongs), func(i, j int) {
						currentLobby.AllSongs[i], currentLobby.AllSongs[j] = currentLobby.AllSongs[j], currentLobby.AllSongs[i]
					})
					slog.Info("Host started the guessing game", "lobby_id", currentLobby.ID, "total_songs", len(currentLobby.AllSongs))
					validTransition = true
				} else if currentLobby.State == StateReveal {
					validTransition = true
				}
				currentLobby.Mutex.Unlock()

				if validTransition {
					playNextSong(currentLobby)
				}
			}

		case "CAST_VOTE":
			if currentLobby == nil {
				continue
			}
			votedFor, ok := msg["voted_for"].(string)
			if !ok {
				continue
			}

			currentLobby.Mutex.Lock()
			if currentLobby.State == StateVoting {
				currentLobby.Votes[userID] = votedFor
				slog.Debug("Vote cast", "voter_id", userID, "voted_for", votedFor, "lobby_id", currentLobby.ID)
			}
			currentLobby.Mutex.Unlock()
			broadcastLobbyState(currentLobby)
		}
	}
}

func playNextSong(lobby *Lobby) {
	lobby.Mutex.Lock()

	if len(lobby.AllSongs) == 0 {
		lobby.State = StateEnd
		lobby.Mutex.Unlock()
		slog.Info("Game ended (no songs left)", "lobby_id", lobby.ID)
		broadcastLobbyState(lobby)
		return
	}

	lobby.CurrentSong = &lobby.AllSongs[0]
	lobby.AllSongs = lobby.AllSongs[1:]
	lobby.State = StateVoting
	lobby.Votes = make(map[string]string)

	lobby.Round++
	currentRound := lobby.Round

	lobby.Mutex.Unlock()
	slog.Info("Started voting phase for new song", "lobby_id", lobby.ID, "round", currentRound, "song_name", lobby.CurrentSong.Name)
	broadcastLobbyState(lobby)

	go func(l *Lobby, expectedRound int) {
		time.Sleep(30 * time.Second)

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
			slog.Info("Voting phase timer expired, moved to Reveal", "lobby_id", l.ID, "round", expectedRound)
			l.Mutex.Unlock()
			broadcastLobbyState(l)
		} else {
			l.Mutex.Unlock()
		}
	}(lobby, currentRound)
}

func broadcastLobbyState(lobby *Lobby) {
	lobby.Mutex.Lock()

	players := make([]string, 0, len(lobby.Users))
	submittedPlayers := make(map[string]bool)
	scores := make(map[string]int)

	usersToBroadcast := make([]*User, 0, len(lobby.Users))

	for id, u := range lobby.Users {
		players = append(players, id)
		submittedPlayers[id] = u.HasSubmitted
		scores[id] = u.Score
		usersToBroadcast = append(usersToBroadcast, u)
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

	lobby.Mutex.Unlock()

	for _, user := range usersToBroadcast {
		user.WriteMutex.Lock()
		err := user.Conn.WriteJSON(stateMsg)
		user.WriteMutex.Unlock()

		if err != nil {
			slog.Error("Failed to broadcast state to user", "user_id", user.ID, "error", err)
		}
	}
}
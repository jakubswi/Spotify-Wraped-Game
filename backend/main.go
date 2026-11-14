package main

import (
	"bytes"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
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
	Connected    bool
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
	ID                 string
	Name               string
	HostID             string
	HostSpotifySession string
	PlaylistID         string
	PlaylistURL        string
	PlaybackError      string
	Users              map[string]*User
	State              GameState
	AllSongs           []Song
	CurrentSong        *Song
	Votes              map[string]string
	Round              int
	CleanupTimer       *time.Timer
	Mutex              sync.Mutex
}

type SpotifyUserSession struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
	DisplayName  string
	Mutex        sync.Mutex
}

var lobbies = make(map[string]*Lobby)
var lobbiesMutex = sync.Mutex{}

var spotifySessions = make(map[string]*SpotifyUserSession)
var spotifySessionsMutex = sync.RWMutex{}

var spotifyAppToken string
var tokenExpiry time.Time
var tokenMutex sync.RWMutex

func getStringField(m map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func getSecret(name string, envFallback string) string {
	data, err := os.ReadFile(fmt.Sprintf("/run/secrets/%s", name))
	if err == nil {
		return strings.TrimSpace(string(data))
	}
	return os.Getenv(envFallback)
}

func generateRandomHex(n int) string {
	b := make([]byte, n)
	if _, err := cryptorand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func getRedirectURI(r *http.Request) string {
	if configured := getSecret("SPOTIFY_REDIRECT_URI", "SPOTIFY_REDIRECT_URI"); configured != "" {
		return configured
	}
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/api/spotify/callback", scheme, r.Host)
}

func isSecureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// App-level Client Credentials token for /api/spotify/search
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

// Host-level Authorization Code token retrieval + auto-refresh
func getValidUserToken(sessionID string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("no spotify session")
	}

	spotifySessionsMutex.RLock()
	session, exists := spotifySessions[sessionID]
	spotifySessionsMutex.RUnlock()
	if !exists {
		return "", fmt.Errorf("spotify session not found")
	}

	session.Mutex.Lock()
	defer session.Mutex.Unlock()

	if session.AccessToken != "" && time.Now().Before(session.Expiry) {
		return session.AccessToken, nil
	}

	if session.RefreshToken == "" {
		return "", fmt.Errorf("no refresh token available")
	}

	clientID := getSecret("CLIENT_ID", "CLIENT_ID")
	clientSecret := getSecret("CLIENT_SECRET", "CLIENT_SECRET")

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", session.RefreshToken)

	req, _ := http.NewRequest("POST", "https://accounts.spotify.com/api/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(clientID+":"+clientSecret)))

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("token refresh failed (%d): %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	token, ok := result["access_token"].(string)
	if !ok || token == "" {
		return "", fmt.Errorf("missing access_token in refresh response")
	}

	session.AccessToken = token
	if expiresIn, ok := result["expires_in"].(float64); ok {
		session.Expiry = time.Now().Add(time.Duration(expiresIn-60) * time.Second)
	} else {
		session.Expiry = time.Now().Add(50 * time.Minute)
	}
	if newRefresh, ok := result["refresh_token"].(string); ok && newRefresh != "" {
		session.RefreshToken = newRefresh
	}

	return session.AccessToken, nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	http.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
	})
	http.HandleFunc("/api/lobbies", handleGetLobbies)
	http.HandleFunc("/api/spotify/search", handleSpotifySearch)
	http.HandleFunc("/api/spotify/login", handleSpotifyLogin)
	http.HandleFunc("/api/spotify/callback", handleSpotifyCallback)
	http.HandleFunc("/api/spotify/status", handleSpotifyStatus)
	http.HandleFunc("/ws", handleConnections)

	slog.Info("Go backend starting on :5000")
	http.ListenAndServe(":5000", nil)
}

func handleSpotifyLogin(w http.ResponseWriter, r *http.Request) {
	clientID := getSecret("CLIENT_ID", "CLIENT_ID")
	if clientID == "" {
		http.Error(w, "Spotify CLIENT_ID not configured", http.StatusInternalServerError)
		return
	}

	state := generateRandomHex(16)
	http.SetCookie(w, &http.Cookie{
		Name:     "spotify_oauth_state",
		Value:    state,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
	})

	scopes := strings.Join([]string{
		"playlist-modify-private",
		"playlist-modify-public",
		"user-modify-playback-state",
		"user-read-playback-state",
		"streaming",
	}, " ")

	params := url.Values{}
	params.Set("client_id", clientID)
	params.Set("response_type", "code")
	params.Set("redirect_uri", getRedirectURI(r))
	params.Set("scope", scopes)
	params.Set("state", state)

	http.Redirect(w, r, "https://accounts.spotify.com/authorize?"+params.Encode(), http.StatusFound)
}

func handleSpotifyCallback(w http.ResponseWriter, r *http.Request) {
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		slog.Warn("Spotify OAuth denied", "error", errParam)
		http.Redirect(w, r, "/?spotify_error="+url.QueryEscape(errParam), http.StatusFound)
		return
	}

	stateCookie, err := r.Cookie("spotify_oauth_state")
	if err != nil || stateCookie.Value == "" || stateCookie.Value != r.URL.Query().Get("state") {
		http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "Missing authorization code", http.StatusBadRequest)
		return
	}

	clientID := getSecret("CLIENT_ID", "CLIENT_ID")
	clientSecret := getSecret("CLIENT_SECRET", "CLIENT_SECRET")

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", getRedirectURI(r))

	req, _ := http.NewRequest("POST", "https://accounts.spotify.com/api/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(clientID+":"+clientSecret)))

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Failed to exchange Spotify token", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		slog.Error("Spotify token exchange failed", "status", resp.StatusCode, "body", string(body))
		http.Error(w, "Spotify token exchange failed", http.StatusBadGateway)
		return
	}

	var tokenResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		http.Error(w, "Invalid token response", http.StatusBadGateway)
		return
	}

	accessToken, _ := tokenResp["access_token"].(string)
	refreshToken, _ := tokenResp["refresh_token"].(string)
	expiresIn, _ := tokenResp["expires_in"].(float64)
	if expiresIn == 0 {
		expiresIn = 3600
	}

	displayName := fetchSpotifyDisplayName(accessToken)
	sessionID := generateRandomHex(24)

	spotifySessionsMutex.Lock()
	spotifySessions[sessionID] = &SpotifyUserSession{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		Expiry:       time.Now().Add(time.Duration(expiresIn-60) * time.Second),
		DisplayName:  displayName,
	}
	spotifySessionsMutex.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     "spotify_session",
		Value:    sessionID,
		Path:     "/",
		MaxAge:   86400 * 7,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, "/", http.StatusFound)
}

func fetchSpotifyDisplayName(accessToken string) string {
	req, _ := http.NewRequest("GET", "https://api.spotify.com/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return ""
	}
	defer resp.Body.Close()

	var profile map[string]interface{}
	if json.NewDecoder(resp.Body).Decode(&profile) == nil {
		if name, ok := profile["display_name"].(string); ok {
			return name
		}
	}
	return ""
}

func handleSpotifyStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	cookie, err := r.Cookie("spotify_session")
	if err != nil || cookie.Value == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{"authenticated": false})
		return
	}

	if _, err := getValidUserToken(cookie.Value); err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"authenticated": false})
		return
	}

	spotifySessionsMutex.RLock()
	session := spotifySessions[cookie.Value]
	displayName := ""
	if session != nil {
		displayName = session.DisplayName
	}
	spotifySessionsMutex.RUnlock()

	json.NewEncoder(w).Encode(map[string]interface{}{
		"authenticated": true,
		"display_name":  displayName,
	})
}

func createSpotifyPlaylist(sessionID, lobbyID string, songs []Song) (string, string, error) {
	token, err := getValidUserToken(sessionID)
	if err != nil {
		return "", "", err
	}

	createPayload := map[string]interface{}{
		"name":        fmt.Sprintf("Wrapped Game - Room %s", lobbyID),
		"description": "Generated automatically during Spotify Wrapped Guessing Game",
		"public":      false,
	}
	bodyBytes, _ := json.Marshal(createPayload)

	req, _ := http.NewRequest("POST", "https://api.spotify.com/v1/me/playlists", bytes.NewReader(bodyBytes))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("create playlist failed (%d): %s", resp.StatusCode, string(errBody))
	}

	var playlistResp struct {
		ID           string            `json:"id"`
		ExternalURLs map[string]string `json:"external_urls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&playlistResp); err != nil {
		return "", "", err
	}

	uris := make([]string, 0, len(songs))
	for _, s := range songs {
		if strings.HasPrefix(s.URI, "spotify:track:") {
			uris = append(uris, s.URI)
		}
	}

	// Add tracks in batches of 100 (Spotify API limit)
	for i := 0; i < len(uris); i += 100 {
		end := i + 100
		if end > len(uris) {
			end = len(uris)
		}
		addPayload, _ := json.Marshal(map[string]interface{}{"uris": uris[i:end]})
		addURL := fmt.Sprintf("https://api.spotify.com/v1/playlists/%s/tracks", url.PathEscape(playlistResp.ID))
		addReq, _ := http.NewRequest("POST", addURL, bytes.NewReader(addPayload))
		addReq.Header.Set("Authorization", "Bearer "+token)
		addReq.Header.Set("Content-Type", "application/json")

		addResp, err := client.Do(addReq)
		if err != nil {
			return playlistResp.ID, playlistResp.ExternalURLs["spotify"], err
		}
		if addResp.StatusCode != http.StatusCreated && addResp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(addResp.Body)
			addResp.Body.Close()
			return playlistResp.ID, playlistResp.ExternalURLs["spotify"], fmt.Errorf("add tracks failed (%d): %s", addResp.StatusCode, string(errBody))
		}
		addResp.Body.Close()
	}

	return playlistResp.ID, playlistResp.ExternalURLs["spotify"], nil
}

func triggerHostPlayback(lobby *Lobby, trackURI string) {
	lobby.Mutex.Lock()
	sessionID := lobby.HostSpotifySession
	lobby.Mutex.Unlock()

	if sessionID == "" || trackURI == "" {
		return
	}

	token, err := getValidUserToken(sessionID)
	if err != nil {
		slog.Warn("Host playback skipped: invalid Spotify token", "err", err)
		return
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"uris": []string{trackURI},
	})

	req, _ := http.NewRequest("PUT", "https://api.spotify.com/v1/me/player/play", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("Spotify play request error", "err", err)
		return
	}
	defer resp.Body.Close()

	var playbackErr string
	if resp.StatusCode == http.StatusNotFound {
		playbackErr = "No active Spotify device found. Open Spotify on your phone or PC to enable auto-play, or use the mini-player below."
	} else if resp.StatusCode == http.StatusForbidden {
		playbackErr = "Spotify Connect auto-play requires Spotify Premium. Use the mini-player below to play the track."
	} else if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		slog.Warn("Spotify play failed", "status", resp.StatusCode, "body", string(body))
		playbackErr = "Could not auto-start Spotify client. Use the mini-player below."
	}

	lobby.Mutex.Lock()
	changed := lobby.PlaybackError != playbackErr
	lobby.PlaybackError = playbackErr
	lobby.Mutex.Unlock()

	if changed {
		broadcastLobbyState(lobby)
	}
}

func pauseHostPlayback(sessionID string) {
	if sessionID == "" {
		return
	}
	token, err := getValidUserToken(sessionID)
	if err != nil {
		return
	}

	req, _ := http.NewRequest("PUT", "https://api.spotify.com/v1/me/player/pause", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
	}
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
	var spotifySessionID string
	if c, err := r.Cookie("spotify_session"); err == nil {
		spotifySessionID = c.Value
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	ws.SetReadDeadline(time.Now().Add(60 * time.Second))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	var userID string
	var currentLobby *Lobby

	defer func() {
    		if currentLobby != nil && userID != "" {
    			currentLobby.Mutex.Lock()
    			if user, exists := currentLobby.Users[userID]; exists && user.Conn == ws {
    				if user.Connected && user.Send != nil {
    					close(user.Send)
    				}
    				user.Send = nil
    				user.Conn = nil
    				user.Connected = false

    				if currentLobby.State == StateLobby {
    					delete(currentLobby.Users, userID)
    				}
    			}

    			connectedCount := 0
    			for _, u := range currentLobby.Users {
    				if u.Connected {
    					connectedCount++
    				}
    			}

    			if connectedCount == 0 {
    				if currentLobby.State == StateLobby {
    					currentLobby.Mutex.Unlock()
    					lobbiesMutex.Lock()
    					delete(lobbies, currentLobby.ID)
    					lobbiesMutex.Unlock()
    				} else {
    					lobbyToClean := currentLobby
    					if lobbyToClean.CleanupTimer != nil {
    						lobbyToClean.CleanupTimer.Stop()
    					}
    					lobbyToClean.CleanupTimer = time.AfterFunc(2*time.Minute, func() {
    						lobbyToClean.Mutex.Lock()
    						stillEmpty := true
    						for _, u := range lobbyToClean.Users {
    							if u.Connected {
    								stillEmpty = false
    								break
    							}
    						}
    						lobbyToClean.Mutex.Unlock()

    						if stillEmpty {
    							lobbiesMutex.Lock()
    							delete(lobbies, lobbyToClean.ID)
    							lobbiesMutex.Unlock()
    							slog.Info("Cleaned up abandoned active lobby", "lobby_id", lobbyToClean.ID)
    						}
    					})
    					currentLobby.Mutex.Unlock()
    				}
    			} else {
    				currentLobby.Mutex.Unlock()
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
            lobbyID := strings.ToUpper(getStringField(msg, "lobby_id"))
            userIDReq := getStringField(msg, "username")
            if lobbyID == "" || userIDReq == "" {
                continue
            }

            userID = userIDReq

            lobbiesMutex.Lock()
            if _, exists := lobbies[lobbyID]; !exists {
                lobbies[lobbyID] = &Lobby{
                    ID:                 lobbyID,
                    HostID:             userID,
                    HostSpotifySession: spotifySessionID,
                    Users:              make(map[string]*User),
                    State:              StateLobby,
                    Votes:              make(map[string]string),
                }
            }
            currentLobby = lobbies[lobbyID]
            lobbiesMutex.Unlock()

            currentLobby.Mutex.Lock()
            if currentLobby.CleanupTimer != nil {
                currentLobby.CleanupTimer.Stop()
                currentLobby.CleanupTimer = nil
            }
            if userID == currentLobby.HostID && spotifySessionID != "" {
                currentLobby.HostSpotifySession = spotifySessionID
            }

            if existingUser, exists := currentLobby.Users[userID]; exists {
                if existingUser.Connected && existingUser.Send != nil {
                    close(existingUser.Send)
                }
                existingUser.Conn = ws
                existingUser.Send = make(chan interface{}, 256)
                existingUser.Connected = true
                go writePump(ws, existingUser.Send)
            } else {
                newUser := &User{
                    ID:        userID,
                    Conn:      ws,
                    Send:      make(chan interface{}, 256),
                    Score:     0,
                    Connected: true,
                }
                currentLobby.Users[userID] = newUser
                go writePump(ws, newUser.Send)
            }
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
				if !ok {
					continue
				}
				uri := getStringField(songMap, "uri")
				parts := strings.Split(uri, ":")
				trackID := ""
				if len(parts) == 3 {
					trackID = parts[2]
				}

				userSongs = append(userSongs, Song{
					ID:      trackID,
					URI:     uri,
					Name:    getStringField(songMap, "name"),
					Artist:  getStringField(songMap, "artist"),
					Img:     getStringField(songMap, "img"),
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
				var shouldCreatePlaylist bool
				var sessionID, lobbyID string
				var songsCopy []Song

				currentLobby.Mutex.Lock()
				if currentLobby.State == StateSubmit {
					currentLobby.AllSongs = []Song{}
					for _, u := range currentLobby.Users {
						if u.HasSubmitted && len(u.Songs) > 0 {
							currentLobby.AllSongs = append(currentLobby.AllSongs, u.Songs...)
						}
					}
					if len(currentLobby.AllSongs) == 0 {
						slog.Warn("START_GAME ignored: no songs submitted yet", "lobby_id", currentLobby.ID)
						currentLobby.Mutex.Unlock()
						continue
					}
					rand.Shuffle(len(currentLobby.AllSongs), func(i, j int) {
						currentLobby.AllSongs[i], currentLobby.AllSongs[j] = currentLobby.AllSongs[j], currentLobby.AllSongs[i]
					})

					if currentLobby.HostSpotifySession != "" && currentLobby.PlaylistID == "" {
						shouldCreatePlaylist = true
						sessionID = currentLobby.HostSpotifySession
						lobbyID = currentLobby.ID
						songsCopy = append([]Song(nil), currentLobby.AllSongs...)
					}
				}
				currentLobby.Mutex.Unlock()

				if shouldCreatePlaylist {
					playlistID, playlistURL, err := createSpotifyPlaylist(sessionID, lobbyID, songsCopy)
					if err != nil {
						slog.Error("Failed to create Spotify playlist", "lobby_id", lobbyID, "err", err)
					} else {
						slog.Info("Created Spotify playlist", "lobby_id", lobbyID, "playlist_id", playlistID, "url", playlistURL)
						currentLobby.Mutex.Lock()
						currentLobby.PlaylistID = playlistID
						currentLobby.PlaylistURL = playlistURL
						currentLobby.Mutex.Unlock()
					}
				}

				playNextSong(currentLobby)
			}

		case "PLAY_CURRENT":
			if currentLobby != nil && userID == currentLobby.HostID {
				currentLobby.Mutex.Lock()
				var uri string
				if currentLobby.State == StateVoting && currentLobby.CurrentSong != nil {
					uri = currentLobby.CurrentSong.URI
				}
				currentLobby.Mutex.Unlock()
				if uri != "" {
					go triggerHostPlayback(currentLobby, uri)
				}
			}

		case "CAST_VOTE":
			if currentLobby == nil {
				continue
			}
			votedFor := getStringField(msg, "voted_for", "vote", "player", "target")
			if votedFor == "" {
				continue
			}

			currentLobby.Mutex.Lock()
			if currentLobby.State == StateVoting && currentLobby.CurrentSong != nil {
				if !strings.EqualFold(currentLobby.CurrentSong.OwnerID, userID) {
					currentLobby.Votes[userID] = votedFor
				}
			}
			currentLobby.Mutex.Unlock()
			broadcastLobbyState(currentLobby)

		case "REVEAL_SONG", "STOP_VOTING", "REVEAL":
			if currentLobby != nil && userID == currentLobby.HostID {
				currentLobby.Mutex.Lock()
				if currentLobby.State == StateVoting && currentLobby.CurrentSong != nil {
					currentLobby.State = StateReveal
					sessionID := currentLobby.HostSpotifySession
					for voter, votedFor := range currentLobby.Votes {
						if strings.EqualFold(strings.TrimSpace(votedFor), strings.TrimSpace(currentLobby.CurrentSong.OwnerID)) {
							if voterUser, exists := currentLobby.Users[voter]; exists {
								voterUser.Score += 100
							}
						}
					}
					currentLobby.Mutex.Unlock()
					broadcastLobbyState(currentLobby)
					go pauseHostPlayback(sessionID)
				} else {
					currentLobby.Mutex.Unlock()
				}
			}

		default:
			slog.Warn("Unknown WebSocket action received", "action", action, "user_id", userID)
		}
	}
}

func writePump(ws *websocket.Conn, send chan interface{}) {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case msg, ok := <-send:
			ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				ws.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := ws.WriteJSON(msg); err != nil {
				return
			}
		case <-ticker.C:
			ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func playNextSong(lobby *Lobby) {
	lobby.Mutex.Lock()
	if len(lobby.AllSongs) == 0 {
		lobby.State = StateEnd
		lobby.PlaybackError = ""
		lobby.Mutex.Unlock()
		broadcastLobbyState(lobby)
		return
	}

	lobby.CurrentSong = &lobby.AllSongs[0]
	lobby.AllSongs = lobby.AllSongs[1:]
	lobby.State = StateVoting
	lobby.Votes = make(map[string]string)
	lobby.PlaybackError = ""
	lobby.Round++
	trackURI := lobby.CurrentSong.URI
	lobby.Mutex.Unlock()

	broadcastLobbyState(lobby)
	go triggerHostPlayback(lobby, trackURI)
}

func broadcastLobbyState(lobby *Lobby) {
	lobby.Mutex.Lock()
	defer lobby.Mutex.Unlock()

	players := []string{}
	connectedPlayers := make(map[string]bool)
	submittedPlayers := make(map[string]bool)
	votedPlayers := make(map[string]bool)
	scores := make(map[string]int)

	for id, u := range lobby.Users {
		players = append(players, id)
		connectedPlayers[id] = u.Connected
		submittedPlayers[id] = u.HasSubmitted
		_, hasVoted := lobby.Votes[id]
		votedPlayers[id] = hasVoted
		scores[id] = u.Score
	}

	stateMsg := map[string]interface{}{
		"type":                   "STATE_UPDATE",
		"state":                  lobby.State,
		"round":                  lobby.Round,
		"host":                   lobby.HostID,
		"host_spotify_connected": lobby.HostSpotifySession != "",
		"playlist_url":           lobby.PlaylistURL,
		"playback_error":         lobby.PlaybackError,
		"current_song":           lobby.CurrentSong,
		"players":                players,
		"connected_players":      connectedPlayers,
		"submitted_players":      submittedPlayers,
		"voted_players":          votedPlayers,
		"scores":                 scores,
	}

	if lobby.State == StateReveal || lobby.State == StateEnd {
		stateMsg["votes"] = lobby.Votes
	}

	for _, user := range lobby.Users {
		if !user.Connected || user.Send == nil {
			continue
		}

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
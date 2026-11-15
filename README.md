# Spotify Wrapped Game

> **Find out who really knows their friends' music taste.** — A real-time multiplayer party guessing game built with **Angular** and **Go**, powered by the **Spotify Web API**.

Players join a shared room, secretly submit five tracks from Spotify, and listen together as the tracks are shuffled and played back. Everyone votes on who picked each mystery song before the owner—and the room's guesses—are revealed. At the end of the match, the game crowns the top "Taste Maker" and automatically saves the room's combined soundtrack as a private Spotify playlist.

## Table of Contents

1. [Gameplay & Game Loop](#gameplay--game-loop)
2. [Technical Architecture](#technical-architecture)
3. [Project Directory Structure](#project-directory-structure)
4. [API & WebSocket Protocol](#api--websocket-protocol)
5. [Configuration & Local Setup](#configuration--local-setup)
6. [CI/CD, Security & GitOps Pipeline](#cicd-security--gitops-pipeline)

---

## Gameplay & Game Loop

### State Machine Flow

The game progresses through five deterministic server-controlled states synchronized across all connected players via WebSockets:

```text
+---------+     Host starts      +----------+     All players     +----------+
|  LOBBY  | -------------------> |  SUBMIT  | ------------------> |  VOTING  |
+---------+    song selection    +----------+    submit 5 songs   +----------+
                                                                     ^    |
                                                     Host cues       |    | Host reveals
                                                     next track      |    v track owner
+---------+              No tracks remaining                      +----------+
|   END   | <---------------------------------------------------- |  REVEAL  |
+---------+                                                       +----------+
```

### How to Play

1. **Create or Join a Room (`LOBBY`):**
   * Players enter a **Room Code** (up to $16$ characters, automatically uppercased) and a **Username** (up to $20$ characters).
   * The first player to join a room code becomes the **Host**. Connecting a Spotify account is recommended for the host to enable automated Spotify Connect playback and playlist generation.
2. **Secret Track Selection (`SUBMIT`):**
   * Once the host starts song picks, every player searches Spotify's catalog and locks in **exactly $5$ tracks**.
   * Picks remain secret until each track's reveal phase.
3. **Mystery Track & Voting (`VOTING`):**
   * The backend shuffles all submitted songs into a master queue.
   * For each round, the track plays via the host's Spotify Connect client (with an embedded Spotify track player fallback).
   * All players—*except the track's submitter*—vote on who picked the song. To prevent cheating, the server strips `owner_id` from the track payload broadcast to non-owners during this phase.
4. **The Reveal & Receipts (`REVEAL`):**
   * The host stops playback and triggers the reveal.
   * Each player who guessed the track owner correctly earns **$100\text{ points}$**, and everyone's individual votes are displayed.
5. **Final Scoreboard & Room Playlist (`END`):**
   * After all tracks have been played, players are ranked by total points, crowning the highest scorer as **The Taste Maker**.
   * If the host authenticated with Spotify, a direct link to the newly generated private Spotify playlist (`Wrapped Game - Room <CODE>`) is displayed on the final screen.

---

## Technical Architecture

### System Overview

```text
+-----------------------------------+          +------------------------------------+
|        Angular Frontend           |          |            Go Backend              |
|  - Standalone Components          |  HTTP    |  - net/http REST Endpoints (:5000) |
|  - RxJS GameState BehaviorSubject | <------> |  - Gorilla WebSocket Hub (/ws)     |
|  - Auto-Reconnecting WS Client    |   WS     |  - Thread-Safe Lobby State Machine |
+-----------------------------------+          +------------------------------------+
                 |                                                |
                 | Embedded Player                                | OAuth2 & Web API
                 v                                                v
+-----------------------------------+          +------------------------------------+
|       Spotify Embed Widget        |          |       Spotify Accounts & API       |
|  open.spotify.com/embed/track/*   |          |  - Client Credentials (Search)     |
+-----------------------------------+          |  - Auth Code Flow (Play/Playlists) |
                                               +------------------------------------+
```

### Backend (`backend/main.go`)

* **Concurrency & State Synchronization:** Each `Lobby` manages its own `sync.Mutex` while global lobby and Spotify session registries are guarded by `sync.Mutex` / `sync.RWMutex`.
* **Resilient WebSocket Lifecycle:**
  * Dedicated per-user buffered send channels (`chan interface{}`) and `writePump` goroutines handle outbound state broadcasts.
  * Heartbeats run on a $25\text{-second}$ ping ticker with a $60\text{-second}$ read deadline.
  * If all players disconnect during an active game (`SUBMIT`, `VOTING`, `REVEAL`), the lobby is preserved for a $2\text{-minute}$ grace period so players can reconnect without losing progress. Empty waiting lobbies (`LOBBY`) are cleaned up immediately.
* **Dual Spotify OAuth2 Strategy:**
  * **Application-Level (`Client Credentials`):** Cached in memory with automatic expiration renewal so any player can search tracks without logging into Spotify.
  * **Host-Level (`Authorization Code`):** Uses CSRF state cookies (`spotify_oauth_state`), `HttpOnly` session cookies (`spotify_session`), and automatic `refresh_token` rotation to control host playback (`PUT /v1/me/player/play` & `/pause`) and batch-create private playlists (`POST /v1/me/playlists`).

### Frontend (`frontend/src/app/`)

* **Angular Standalone Architecture:** Built with standalone components (`AppComponent`, `GameComponent`) and template-driven forms.
* **Reactive State Management (`GameService`):** Wraps WebSocket lifecycle management in an RxJS `BehaviorSubject<GameState | null>`, handling automatic reconnection with a $2\text{-second}$ backoff and preserving local track metadata across mid-round updates.
* **Spotify Integration (`SpotifyService`):** Interfaces with backend auth/search endpoints and sanitizes track embed URLs via `DomSanitizer` for the host's mini-player fallback.

---

## Project Directory Structure

```text
spotify-wrapped-game/
├── .github/
│   ├── workflows/
│   │   └── pipeline.yaml            # CI/CD: SonarCloud, Trivy, Cosign & ArgoCD GitOps
│   └── dependabot.yml               # Automated weekly updates (Go, npm, Docker, Actions)
├── backend/
│   ├── go.mod
│   ├── go.sum
│   └── main.go                      # Go HTTP/WebSocket server, game engine & Spotify API client
├── docker/
│   ├── backend/
│   │   └── Dockerfile               # Backend container build definition
│   └── frontend/
│       └── Dockerfile               # Frontend container build definition
└── frontend/
    ├── package.json
    └── src/app/
        ├── app.component.ts         # Root standalone component
        ├── game/
        │   ├── game.component.ts    # Game UI state machine, voting & Spotify embed logic
        │   └── game.component.html  # Multi-stage views (Entry, Lobby, Submit, Voting, Reveal, End)
        └── services/
            ├── game.service.ts      # WebSocket client, auto-reconnect & GameState stream
            └── spotify.service.ts   # HTTP client for track search & OAuth status/login
```

---

## API & WebSocket Protocol

### HTTP Endpoints

| Method | Endpoint | Description |
| :--- | :--- | :--- |
| `GET` | `/api/health` | Health check returning `{"status": "healthy"}`. |
| `GET` | `/api/lobbies` | Lists active lobbies with ID, host, player count, and current state. |
| `GET` | `/api/spotify/search?q=<query>` | Searches Spotify tracks (limit $10$) using the server's Client Credentials token. |
| `GET` | `/api/spotify/login` | Initiates the Spotify Authorization Code OAuth2 redirect for hosts. |
| `GET` | `/api/spotify/callback` | Handles the Spotify OAuth2 callback, exchanges tokens, and sets `spotify_session`. |
| `GET` | `/api/spotify/status` | Checks whether the current browser session has a valid Spotify host token. |

### WebSocket Protocol (`/ws`)

#### Client-to-Server Actions

| Action | Sender | Payload Fields | Description |
| :--- | :--- | :--- | :--- |
| `JOIN_LOBBY` | Any | `lobby_id`, `username` | Joins an existing room or creates a new one as host. |
| `START_SUBMISSION` | Host | — | Transitions the room from `LOBBY` to `SUBMIT`. |
| `SUBMIT_SONGS` | Any | `songs` (`Song[]`) | Locks in the player's $5$ selected tracks. |
| `START_GAME` / `NEXT_SONG` | Host | — | Shuffles tracks (and creates the Spotify playlist on first start) or advances to the next track. |
| `PLAY_CURRENT` | Host | — | Retries triggering playback of `current_song` on the host's active Spotify device. |
| `CAST_VOTE` | Non-Owner | `player` | Records a guess for who submitted the active track. |
| `REVEAL_SONG` | Host | — | Pauses host playback, scores $+100\text{ pts}$ per correct guess, and moves to `REVEAL`. |

#### Server-to-Client Broadcast (`STATE_UPDATE`)

Broadcast whenever lobby membership, game phase, votes, or playback status changes:
* `state`: `'LOBBY' | 'SUBMIT' | 'VOTING' | 'REVEAL' | 'END'`
* `round`, `host`, `host_spotify_connected`, `playlist_url`, `playback_error`
* `current_song`: Track metadata (`owner_id` is redacted for all players except the track's submitter during `VOTING`)
* `players`, `connected_players`, `submitted_players`, `voted_players`, `scores`
* `votes`: Included only during `REVEAL` and `END` phases

---

## Configuration & Local Setup

### 1. Spotify Developer App Setup

1. Create an application in the [Spotify Developer Dashboard](https://developer.spotify.com/dashboard).
2. Add your callback URL to **Redirect URIs** (e.g., `http://localhost:4200/api/spotify/callback` or your production domain).
3. Note your **Client ID** and **Client Secret**. The host login requests the following scopes:
   * `playlist-modify-private`, `playlist-modify-public`
   * `user-modify-playback-state`, `user-read-playback-state`, `streaming`

### 2. Environment Variables & Secrets

The Go backend resolves secrets from Docker/Kubernetes secret mounts (`/run/secrets/<NAME>`) first, falling back to standard environment variables:

| Variable / Secret Name | Required | Description |
| :--- | :--- | :--- |
| `CLIENT_ID` | Yes | Spotify Application Client ID. |
| `CLIENT_SECRET` | Yes | Spotify Application Client Secret. |
| `SPOTIFY_REDIRECT_URI` | Optional | Explicit OAuth callback URL (auto-inferred from `Host` / `X-Forwarded-Proto` if omitted). |

### 3. Running Locally

**Start the Go Backend (Port `5000`):**
```bash
cd backend
export CLIENT_ID="your_spotify_client_id"
export CLIENT_SECRET="your_spotify_client_secret"
go run main.go
```

**Start the Angular Frontend:**
```bash
cd frontend
npm ci
npm start
```
*(Ensure your local reverse proxy or Angular proxy configuration forwards `/api` and `/ws` requests to `http://localhost:5000`.)*

---

## CI/CD, Security & GitOps Pipeline

Automated workflows in `.github/workflows/pipeline.yaml` run on every push and pull request targeting `master`:

1. **Code Quality & Static Analysis (`sonar-scan`):**
   * Runs `go vet ./...` and `go test -race -coverprofile=coverage.out ./...` on the Go backend.
   * Builds the Angular frontend in production mode (`npm run build -- --configuration=production`).
   * Executes a **SonarCloud** code quality and coverage scan.
2. **Container Build, Vulnerability Gate & Signing (`build-and-push`):**
   * Builds both `backend` and `frontend` Docker images via Buildx.
   * Scans image tarballs with **Trivy**, uploads SARIF findings to the GitHub Security tab, and blocks builds containing unfixed `CRITICAL` or `HIGH` vulnerabilities.
   * On `master` merges, pushes images to **GitHub Container Registry (GHCR)** with **SBOM** and **provenance** attestations, and signs images using keyless OIDC **Cosign**.
3. **ArgoCD GitOps Deployment (`deploy`):**
   * Checks out the external homelab GitOps repository, updates `.backend.image.tag` and `.frontend.image.tag` in `gitops/apps/spotify-wrapped/values.yaml` with the commit SHA via `yq`, and pushes the commit for **ArgoCD** synchronization.
4. **Dependency Maintenance:**
   * `.github/dependabot.yml` checks weekly for updates across GitHub Actions, Go modules (`/backend`), npm packages (`/frontend`), and Docker base images.
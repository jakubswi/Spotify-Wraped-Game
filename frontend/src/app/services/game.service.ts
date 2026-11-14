import { Injectable, OnDestroy } from '@angular/core';
import { BehaviorSubject } from 'rxjs';

export interface Song {
  id: string;
  uri: string;
  name: string;
  artist: string;
  img: string;
  owner_id: string;
}

export interface GameState {
  state: 'LOBBY' | 'SUBMIT' | 'VOTING' | 'REVEAL' | 'END';
  round?: number;
  host: string;
  host_spotify_connected?: boolean;
  playlist_url?: string;
  playback_error?: string;
  current_song?: Song;
  players: string[];
  submitted_players: Record<string, boolean>;
  voted_players?: Record<string, boolean>;
  scores?: Record<string, number>;
  votes?: Record<string, string>;
}

@Injectable({ providedIn: 'root' })
export class GameService implements OnDestroy {
  private ws: WebSocket | null = null;

  public gameState$ = new BehaviorSubject<GameState | null>(null);
  public username = '';

  private getWebSocketUrl(): string {
    const protocol =
      window.location.protocol === 'https:' ? 'wss:' : 'ws:';

    return `${protocol}//${window.location.host}/ws`;
  }

  connect(lobbyId: string, username: string) {
    const cleanLobbyId = lobbyId.trim().toUpperCase();
    const cleanUsername = username.trim();

    if (!cleanLobbyId || !cleanUsername) return;

    if (this.ws) {
      this.disconnect();
    }

    this.username = cleanUsername;
    this.ws = new WebSocket(this.getWebSocketUrl());

    this.ws.onopen = () => {
      this.sendAction('JOIN_LOBBY', {
        lobby_id: cleanLobbyId,
        username: cleanUsername
      });
    };

    this.ws.onmessage = (event) => {
      try {
        const data: GameState & { type?: string } = JSON.parse(event.data);
        if (data.type === 'STATE_UPDATE') {
          const prev = this.gameState$.value;

          // Preserve the same current_song reference during mid-round vote updates
          // so Angular does not reset or reload the host's music player.
          if (
            prev?.current_song &&
            data.current_song &&
            prev.round === data.round &&
            prev.current_song.uri === data.current_song.uri &&
            prev.current_song.owner_id === data.current_song.owner_id
          ) {
            data.current_song = prev.current_song;
          }

          this.gameState$.next(data);
        }
      } catch (e) {
        console.error('Failed to parse WebSocket payload', e);
      }
    };

    this.ws.onclose = () => {
      console.warn('WebSocket disconnected.');
      this.ws = null;
    };

    this.ws.onerror = (error) => {
      console.error('WebSocket encountered an error:', error);
    };
  }

  sendAction(action: string, payload: any = {}) {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ action, ...payload }));
    } else {
      console.warn(`Dropped action [${action}]: WebSocket is not open.`);
    }
  }

  disconnect() {
    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
    this.gameState$.next(null);
  }

  ngOnDestroy() {
    this.disconnect();
  }
}

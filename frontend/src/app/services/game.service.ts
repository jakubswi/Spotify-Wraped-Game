import { Injectable, OnDestroy } from '@angular/core';
import { BehaviorSubject } from 'rxjs';
import { environment } from '../../environments/environment';

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
  host: string;
  current_song?: Song;
  players: string[];
  submitted_players: Record<string, boolean>;
  scores?: Record<string, number>;
  votes?: Record<string, string>;
}

@Injectable({ providedIn: 'root' })
export class GameService implements OnDestroy {
  private ws: WebSocket | null = null;
  public gameState$ = new BehaviorSubject<GameState | null>(null);
  public username: string = '';

  connect(lobbyId: string, username: string) {
    if (this.ws && (this.ws.readyState === WebSocket.CONNECTING || this.ws.readyState === WebSocket.OPEN)) {
      console.warn('WebSocket is already connected.');
      return;
    }

    this.username = username;
    this.ws = new WebSocket(environment.wsUrl);

    this.ws.onopen = () => {
      this.sendAction('JOIN_LOBBY', { lobby_id: lobbyId, username });
    };

    this.ws.onmessage = (event) => {
      try {
        const data = JSON.parse(event.data);
        if (data.type === 'STATE_UPDATE') {
          this.gameState$.next(data);
        }
      } catch (e) {
        console.error('Failed to parse WebSocket payload', e);
      }
    };

    this.ws.onclose = () => {
      console.log('WebSocket disconnected.');
      // Future enhancement: Add automatic reconnection logic or trigger a UI warning here
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

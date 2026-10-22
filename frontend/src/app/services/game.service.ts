import { Injectable } from '@angular/core';
import { BehaviorSubject } from 'rxjs';

export interface GameState {
  state: string;
  players: string[];
  host: string;
  current_song?: {
    id: string;
    name: string;
    owner_id: string;
  } | null;
  scores?: Record<string, number>;
}

@Injectable({
  providedIn: 'root'
})
export class GameService {
  socket?: WebSocket;
  lobbyId?: string;
  username?: string;
  gameState$ = new BehaviorSubject<GameState | null>(null);

  connect(lobbyId: string, username: string) {
    this.lobbyId = lobbyId;
    this.username = username;

    const wsUrl = `ws://${window.location.host}/ws`;
    this.socket = new WebSocket(wsUrl);

    this.socket.onopen = () => {
      this.sendMessage({ action: 'JOIN_LOBBY', lobby_id: lobbyId, username: username });
    };

    this.socket.onmessage = (event) => {
      const data = JSON.parse(event.data);
      this.gameState$.next(data as GameState);
    };

    this.socket.onclose = () => {
      setTimeout(() => {
        if (this.lobbyId && this.username) {
          this.connect(this.lobbyId, this.username);
        }
      }, 3000);
    };
  }

  sendMessage(msg: any) {
    if (this.socket && this.socket.readyState === WebSocket.OPEN) {
      this.socket.send(JSON.stringify(msg));
    }
  }

  startSubmission() {
    this.sendMessage({ action: 'START_SUBMISSION' });
  }

  submitSongs(songs: any[]) {
    this.sendMessage({ action: 'SUBMIT_SONGS', songs });
  }

  startGame() {
    this.sendMessage({ action: 'START_GAME' });
  }

  castVote(votedFor: string) {
    this.sendMessage({ action: 'CAST_VOTE', voted_for: votedFor });
  }
}

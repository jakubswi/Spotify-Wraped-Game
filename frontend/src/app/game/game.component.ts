import { Component, OnDestroy, OnInit } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { GameService, GameState } from '../services/game.service';
import { SpotifyService } from '../services/spotify.service';
import { Subscription } from 'rxjs';
import { DomSanitizer, SafeResourceUrl } from '@angular/platform-browser';

@Component({
  selector: 'app-game',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: './game.component.html'
})
export class GameComponent implements OnInit, OnDestroy {
  username = '';
  lobbyId = '';

  state: GameState | null = null;
  private stateSub!: Subscription;

  searchQuery = '';
  searchResults: any[] = [];
  selectedSongs: any[] = [];
  hasSubmitted = false;
  hasVoted = false;
  isSearching = false;
  searchError = '';

  spotifyAuthenticated = false;
  spotifyDisplayName = '';
  spotifyEmbedUrl: SafeResourceUrl | null = null;
  private currentEmbedTrackId = '';

  constructor(
    public gameService: GameService,
    private spotifyService: SpotifyService,
    private sanitizer: DomSanitizer
  ) {}

  ngOnInit(): void {
    this.spotifyService.checkAuthStatus().subscribe({
      next: status => {
        this.spotifyAuthenticated = status.authenticated;
        this.spotifyDisplayName = status.display_name || '';
      },
      error: () => {
        this.spotifyAuthenticated = false;
      }
    });

    this.stateSub = this.gameService.gameState$.subscribe(newState => {
      if (!newState) {
        this.state = null;
        this.spotifyEmbedUrl = null;
        this.currentEmbedTrackId = '';
        return;
      }

      if (this.state?.state !== newState.state) {
        if (newState.state === 'SUBMIT') {
          this.hasSubmitted = false;
          this.hasVoted = false;
          this.selectedSongs = [];
          this.searchResults = [];
          this.searchQuery = '';
        }
        if (newState.state === 'VOTING') {
          this.hasVoted = false;
        }
      }

      if (newState.state === 'SUBMIT' && newState.submitted_players?.[this.gameService.username]) {
        this.hasSubmitted = true;
      }
      if (newState.state === 'VOTING' && newState.voted_players?.[this.gameService.username]) {
        this.hasVoted = true;
      }

      const trackId = newState.current_song?.id ?? '';
      if (trackId && trackId !== this.currentEmbedTrackId) {
        this.currentEmbedTrackId = trackId;
        const spotifyIdPattern = /^[a-zA-Z0-9]{22}$/;
        if (!spotifyIdPattern.test(trackId)) {
          throw new Error('Invalid Spotify track ID');
        }
        const url = `https://open.spotify.com/embed/track/${trackId}`;
        this.spotifyEmbedUrl = this.sanitizer.bypassSecurityTrustResourceUrl(url);
      } else if (!trackId) {
        this.currentEmbedTrackId = '';
        this.spotifyEmbedUrl = null;
      }

      this.state = newState;
    });
  }

  ngOnDestroy(): void {
    if (this.stateSub) this.stateSub.unsubscribe();
  }

  get isHost(): boolean {
    return this.state?.host === this.gameService.username;
  }

  get votesCastCount(): number {
    return Object.values(this.state?.voted_players ?? {}).filter(Boolean).length;
  }

  get submissionsReadyCount(): number {
    return Object.values(this.state?.submitted_players ?? {}).filter(Boolean).length;
  }

  get everyoneSubmitted(): boolean {
    const players = this.state?.players ?? [];
    const connected = this.state?.connected_players;
    const activePlayers = connected ? players.filter(p => connected[p] !== false) : players;
    return activePlayers.length > 0 && activePlayers.every(player => this.state?.submitted_players?.[player] === true);
  }

  get eligibleVotersCount(): number {
    const players = this.state?.players ?? [];
    const connected = this.state?.connected_players;
    const activePlayers = connected ? players.filter(p => connected[p] !== false) : players;
    return activePlayers.length > 1 ? activePlayers.length - 1 : 0;
  }

  get rankedPlayers(): string[] {
    const players = [...(this.state?.players ?? [])];
    const scores = this.state?.scores ?? {};
    return players.sort((a, b) => (scores[b] ?? 0) - (scores[a] ?? 0));
  }

  getInitials(name: string): string {
    const words = name.trim().split(/\s+/).filter(Boolean);
    if (words.length === 0) return '?';
    return words.slice(0, 2).map(word => word.charAt(0).toUpperCase()).join('');
  }

  avatarTone(index: number): string {
    return `avatar-tone-${index % 5 + 1}`;
  }

  connectSpotify(): void {
    this.spotifyService.loginWithSpotify();
  }

  join(): void {
    const cleanUsername = this.username.trim();
    const cleanLobbyId = this.lobbyId.trim().toUpperCase();
    if (!cleanUsername || !cleanLobbyId) return;

    this.username = cleanUsername;
    this.lobbyId = cleanLobbyId;
    this.gameService.connect(cleanLobbyId, cleanUsername);
  }

  searchSpotify(): void {
    const query = this.searchQuery.trim();
    if (!query || this.isSearching) return;

    this.isSearching = true;
    this.searchError = '';
    this.spotifyService.searchTracks(query).subscribe({
      next: results => {
        this.searchResults = results;
        this.isSearching = false;
        if (results.length === 0) this.searchError = 'No tracks found. Try another song or artist.';
      },
      error: () => {
        this.searchResults = [];
        this.isSearching = false;
        this.searchError = 'Spotify search is unavailable right now. Please try again.';
      }
    });
  }

  hasSong(song: any): boolean {
    return this.selectedSongs.some(selected => selected.uri === song.uri);
  }

  addSong(song: any): void {
    if (this.selectedSongs.length >= 5 || this.hasSong(song)) return;
    this.selectedSongs = [...this.selectedSongs, song];
  }

  removeSong(song: any): void {
    this.selectedSongs = this.selectedSongs.filter(selected => selected.uri !== song.uri);
  }

  submitMySongs(): void {
    if (this.selectedSongs.length !== 5) return;
    this.gameService.sendAction('SUBMIT_SONGS', { songs: this.selectedSongs });
    this.hasSubmitted = true;
  }

  startSubmission(): void {
    this.gameService.sendAction('START_SUBMISSION');
  }

  startGame(): void {
    this.gameService.sendAction('START_GAME');
  }

  retryPlayback(): void {
    if (!this.isHost) return;
    this.gameService.sendAction('PLAY_CURRENT');
  }

  revealSong(): void {
    if (!this.isHost) return;
    this.gameService.sendAction('REVEAL_SONG');
  }

  voteForPlayer(player: string): void {
    if (this.hasVoted || player === this.gameService.username) return;
    this.gameService.sendAction('CAST_VOTE', { player });
    this.hasVoted = true;
  }
}

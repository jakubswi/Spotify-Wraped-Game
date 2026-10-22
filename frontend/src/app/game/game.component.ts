import { Component, OnInit, OnDestroy } from '@angular/core';
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

  constructor(
    public gameService: GameService,
    private spotifyService: SpotifyService,
    private sanitizer: DomSanitizer
  ) {}

  getSpotifyEmbedUrl(trackId: string | undefined): SafeResourceUrl | string {
      if (!trackId) return '';
      const url = `https://open.spotify.com/embed/track/${trackId}?utm_source=generator`;
      return this.sanitizer.bypassSecurityTrustResourceUrl(url);
    }

  ngOnInit() {
      this.stateSub = this.gameService.gameState$.subscribe(newState => {
        if (!newState) {
          this.state = null;
          return;
        }

        if (this.state?.state !== newState.state) {
          if (newState.state === 'SUBMIT') this.hasSubmitted = false;
          if (newState.state === 'VOTING') this.hasVoted = false;
        }

        this.state = newState;
      });
    }

  ngOnDestroy() {
    if (this.stateSub) this.stateSub.unsubscribe();
  }

  get isHost(): boolean {
    return this.state?.host === this.gameService.username;
  }

  join() {
    if (this.username && this.lobbyId) {
      this.gameService.connect(this.lobbyId, this.username);
    }
  }

  searchSpotify() {
    if (!this.searchQuery) return;
    this.spotifyService.searchTracks(this.searchQuery).subscribe(results => {
      this.searchResults = results;
    });
  }

  addSong(song: any) {
    if (!this.selectedSongs.find(s => s.uri === song.uri)) {
      this.selectedSongs.push(song);
    }
  }

  removeSong(song: any) {
    this.selectedSongs = this.selectedSongs.filter(s => s.uri !== song.uri);
  }

  submitMySongs() {
    this.gameService.submitSongs(this.selectedSongs);
    this.hasSubmitted = true;
  }

  voteForPlayer(player: string) {
    if (this.hasVoted || player === this.gameService.username) return;
    this.gameService.castVote(player);
    this.hasVoted = true;
  }
}

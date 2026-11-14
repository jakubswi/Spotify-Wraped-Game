import { Injectable } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { Observable } from 'rxjs';
import { map } from 'rxjs/operators';

export interface SpotifyAuthStatus {
  authenticated: boolean;
  display_name?: string;
}

@Injectable({ providedIn: 'root' })
export class SpotifyService {
  private apiUrl = '/api/spotify/search';

  constructor(private http: HttpClient) {}

  searchTracks(query: string): Observable<any[]> {
    return this.http.get<any>(`${this.apiUrl}?q=${encodeURIComponent(query)}`).pipe(
      map(response => {
        const items = response?.tracks?.items || [];
        return items.map((item: any) => ({
          uri: item.uri,
          name: item.name,
          artist: item.artists.map((a: any) => a.name).join(', '),
          img: item.album?.images[0]?.url || ''
        }));
      })
    );
  }

  checkAuthStatus(): Observable<SpotifyAuthStatus> {
    return this.http.get<SpotifyAuthStatus>('/api/spotify/status');
  }

  loginWithSpotify(): void {
    window.location.href = '/api/spotify/login';
  }
}

import { Component } from '@angular/core';
import { GameComponent } from './game/game.component';
import { GameService, GameState } from './services/game.service';

@Component({
  selector: 'app-root',
  standalone: true,
  imports: [GameComponent],
  template: '<app-game></app-game>'
})
export class AppComponent {}

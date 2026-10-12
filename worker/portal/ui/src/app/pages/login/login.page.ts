import { ChangeDetectionStrategy, Component, inject, signal } from '@angular/core';

import { IconComponent } from '../../components/icon/icon.component';
import { AuthService } from '../../global/services/auth/auth.service';
import { message } from '../../global/services/control/control.service';

/** Shown instead of the application while nobody is signed in. */
@Component({
  selector: 'app-login',
  imports: [IconComponent],
  template: `
    <main class="login">
      <form class="card login-card" (submit)="$event.preventDefault(); submit()" novalidate>
        @if (error()) {
          <div class="banner error" role="alert"><app-icon name="alert" /> {{ error() }}</div>
        }

        <!-- Lets password managers store the password for this site. -->
        <input class="sr-only" name="username" value="portal" autocomplete="username" tabindex="-1" aria-hidden="true" readonly />
        <div class="field">
          <label for="password">Password</label>
          <input
            #pw
            id="password"
            name="password"
            type="password"
            autocomplete="current-password"
            required
            autofocus
            [value]="password()"
            (input)="password.set(pw.value)"
          />
        </div>

        <button type="submit" class="primary" [disabled]="busy() || !password()">
          {{ busy() ? 'Signing in…' : 'Sign in' }}
        </button>
      </form>
    </main>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class LoginPage {
  private readonly auth = inject(AuthService);

  protected readonly password = signal('');
  protected readonly busy = signal(false);
  protected readonly error = signal('');

  protected async submit(): Promise<void> {
    if (this.busy() || !this.password()) {
      return;
    }
    this.busy.set(true);
    this.error.set('');
    try {
      await this.auth.login(this.password());
    } catch (error) {
      this.password.set('');
      this.error.set(message(error));
    } finally {
      this.busy.set(false);
    }
  }
}

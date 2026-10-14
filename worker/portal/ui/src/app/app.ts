import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  inject,
  signal,
} from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { NavigationEnd, Router, RouterLink, RouterOutlet } from '@angular/router';
import { filter, map } from 'rxjs';

import { ConfirmDialogComponent } from './components/confirm-dialog/confirm-dialog.component';
import { IconComponent } from './components/icon/icon.component';
import { HistoryPage } from './pages/history/history.page';
import { SnapshotsPage } from './pages/snapshots/snapshots.page';
import { DashboardPage } from './pages/dashboard/dashboard.page';
import { ToastsComponent } from './components/toasts/toasts.component';
import { LoginPage } from './pages/login/login.page';
import { SetupPage } from './pages/setup/setup.page';
import { AuthService } from './global/services/auth/auth.service';
import { ControlService } from './global/services/control/control.service';

function readTheme(): 'light' | 'dark' | null {
  try {
    const stored = localStorage.getItem('theme');
    return stored === 'light' || stored === 'dark' ? stored : null;
  } catch {
    return null;
  }
}

@Component({
  selector: 'app-root',
  imports: [
    RouterOutlet,
    RouterLink,
    IconComponent,
    ToastsComponent,
    ConfirmDialogComponent,
    DashboardPage,
    HistoryPage,
    SnapshotsPage,
    LoginPage,
    SetupPage,
  ],
  templateUrl: './app.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class App {
  protected readonly control = inject(ControlService);
  protected readonly auth = inject(AuthService);
  /** Latched, the setup stays open after storing the password until the admin finishes it. */
  protected readonly setupOpen = signal(false);
  private readonly router = inject(Router);

  private readonly url = toSignal(
    this.router.events.pipe(
      filter((event) => event instanceof NavigationEnd),
      map(() => this.router.url.split(/[?#]/)[0]),
    ),
    { initialValue: this.router.url.split(/[?#]/)[0] },
  );
  /** Everything but the overview opens in the drawer. */
  protected readonly drawerOpen = computed(() => this.url() !== '/' && this.url() !== '');
  /** Run output, snapshot content and the history need more room than forms. */
  protected readonly drawerWide = computed(() => /^\/(runs|snapshots)/.test(this.url()));

  /** `null` follows the system preference until the user picks a theme. */
  protected readonly theme = signal<'light' | 'dark' | null>(readTheme());
  private readonly systemDark = matchMedia('(prefers-color-scheme: dark)');
  protected readonly dark = computed(() => (this.theme() ?? (this.systemDark.matches ? 'dark' : 'light')) === 'dark');

  constructor() {
    effect(() => {
      if (this.control.needsSetup()) {
        this.setupOpen.set(true);
      }
    });
    effect(() => {
      const theme = this.theme();
      if (theme) {
        document.documentElement.dataset['theme'] = theme;
      } else {
        delete document.documentElement.dataset['theme'];
      }
    });
    document.addEventListener('keydown', (event) => {
      if (event.key === 'Escape' && this.drawerOpen() && !document.querySelector('dialog[open]')) {
        this.close();
      }
    });
    // Material-style ripple on every button.
    document.addEventListener('pointerdown', (event) => {
      const button = (event.target as Element | null)?.closest<HTMLElement>('button:not(:disabled), a.button');
      if (!button || matchMedia('(prefers-reduced-motion: reduce)').matches) {
        return;
      }
      const rect = button.getBoundingClientRect();
      const size = Math.max(rect.width, rect.height) * 2;
      const ripple = document.createElement('span');
      ripple.className = 'ripple';
      ripple.style.cssText = `width:${size}px;height:${size}px;left:${event.clientX - rect.left - size / 2}px;top:${event.clientY - rect.top - size / 2}px`;
      button.append(ripple);
      setTimeout(() => ripple.remove(), 650);
    });
  }

  protected toTop(): void {
    if (this.drawerOpen()) {
      this.close();
    }
    scrollTo({ top: 0, behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' });
  }

  protected async signOut(): Promise<void> {
    await this.auth.logout();
    void this.router.navigateByUrl('/');
  }

  protected close(): void {
    void this.router.navigateByUrl('/');
  }

  protected toggleTheme(): void {
    const next = this.dark() ? 'light' : 'dark';
    this.theme.set(next);
    try {
      localStorage.setItem('theme', next);
    } catch {
      // Storage may be blocked, the choice then only lasts for this visit.
    }
  }
}

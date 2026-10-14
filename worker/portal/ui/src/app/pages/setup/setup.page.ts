import { ChangeDetectionStrategy, Component, computed, inject, output, signal } from '@angular/core';

import { IconComponent } from '../../components/icon/icon.component';
import { ControlService, message } from '../../global/services/control/control.service';
import { ToastService } from '../../global/services/toast/toast.service';

type Step = 'password' | 'stored';

/**
 * Shown instead of the overview while the daemon has no repository password, which is how a new installation
 * starts. It stores that password before the first backup runs.
 */
@Component({
  selector: 'app-setup',
  imports: [IconComponent],
  template: `
    <main class="login">
      <div class="card login-card setup">
        <header class="setup-head">
          <span class="logo"><app-icon name="inventory_2" /></span>
          <div>
            <h1>Set up your backup</h1>
            <p class="muted">One step before the first run.</p>
          </div>
        </header>

        @if (error()) {
          <div class="banner error" role="alert"><app-icon name="warning" /> {{ error() }}</div>
        }

        @switch (step()) {
          @case ('password') {
            <p class="muted">
              Backups are encrypted with a repository password, kept encrypted on the daemon as the secret
              <strong>{{ secretName() }}</strong
              >. Without it nothing can be restored, so keep a copy somewhere safe.
            </p>
            <form class="form" (submit)="$event.preventDefault(); store()" novalidate>
              <label class="check option" [class.selected]="generate()">
                <input #gen type="checkbox" [checked]="generate()" (change)="generate.set(gen.checked)" />
                <span>
                  <strong>Generate a strong password</strong>
                  <span class="hint">Recommended for a new repository. It is shown once.</span>
                </span>
              </label>
              @if (!generate()) {
                <div class="field">
                  <label for="s-value">Password</label>
                  <input #v1 id="s-value" type="password" autocomplete="new-password" [value]="value()" (input)="value.set(v1.value)" />
                  <span class="hint">Use the existing password to open a repository that already exists. A single line.</span>
                </div>
                <div class="field">
                  <label for="s-again">Repeat</label>
                  <input #v2 id="s-again" type="password" autocomplete="new-password" [value]="again()" (input)="again.set(v2.value)" />
                  @if (again() && !matches()) {
                    <span class="field-error">The passwords differ.</span>
                  }
                </div>
              }
              <div class="form-actions">
                <button type="submit" class="primary" [disabled]="busy() || !valid()">
                  <app-icon name="lock" /> {{ busy() ? 'Storing…' : 'Store password' }}
                </button>
              </div>
            </form>
          }
          @case ('stored') {
            <div class="banner warn" role="status">
              <app-icon name="warning" />
              <div>
                This is the only time the password is shown. Store it in your password manager now, without it the
                repository cannot be restored.
              </div>
            </div>
            <div class="secret">
              <pre class="mono secret-value">{{ generated() }}</pre>
              <button type="button" class="secondary small" (click)="copy()"><app-icon name="content_copy" /> Copy</button>
            </div>
            <label class="check option" [class.selected]="acknowledged()">
              <input #ack type="checkbox" [checked]="acknowledged()" (change)="acknowledged.set(ack.checked)" />
              <span><strong>I have stored the password</strong></span>
            </label>
            <div class="form-actions">
              <button type="button" class="primary" [disabled]="!acknowledged()" (click)="finish()">Finish <app-icon name="check" /></button>
            </div>
          }
        }
      </div>
    </main>
  `,
  styles: `
    .setup {
      width: min(36rem, 100%);
      gap: 1.25rem;
      padding: 1.75rem;
      box-shadow: var(--shadow-lg);
      animation: setup-in 0.5s var(--ease) both;
    }
    @keyframes setup-in {
      from {
        opacity: 0;
        transform: translateY(12px);
      }
    }
    .setup-head {
      display: flex;
      align-items: center;
      gap: 0.9rem;
    }
    .setup-head .logo {
      width: 2.75rem;
      height: 2.75rem;
      border-radius: 14px;
      flex: none;
    }
    .setup-head h1 {
      font-size: 1.5rem;
    }
    .option {
      margin-top: 0;
      padding: 0.8rem 1rem;
      border: 1px solid var(--line);
      border-radius: 12px;
      background: var(--surface);
      transition: border-color 0.2s, background 0.2s;
    }
    .option:hover {
      border-color: color-mix(in srgb, var(--accent) 45%, var(--line));
    }
    .option.selected {
      border-color: var(--accent);
      background: var(--accent-soft);
    }
    .option input {
      accent-color: var(--accent);
    }
    .secret {
      display: flex;
      align-items: flex-start;
      gap: 0.6rem;
    }
    .secret-value {
      flex: 1;
      min-width: 0;
      margin: 0;
      padding: 0.7rem 0.9rem;
      border: 1px dashed var(--accent);
      border-radius: 12px;
      background: var(--output-bg);
      color: var(--output-fg);
      user-select: all;
      word-break: break-all;
      white-space: pre-wrap;
    }
    @media (max-width: 520px) {
      .setup {
        padding: 1.25rem;
      }
      .secret {
        flex-direction: column;
        align-items: stretch;
      }
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class SetupPage {
  private readonly control = inject(ControlService);
  private readonly toasts = inject(ToastService);

  /** The admin is done, the overview can open. */
  readonly done = output<void>();

  protected readonly step = signal<Step>('password');
  protected readonly secretName = computed(() => this.control.defaults()?.passwordSecret ?? '');

  protected readonly generate = signal(true);
  protected readonly value = signal('');
  protected readonly again = signal('');
  protected readonly matches = computed(() => this.value() === this.again());
  protected readonly valid = computed(() => this.generate() || (!!this.value() && this.matches()));

  protected readonly busy = signal(false);
  protected readonly error = signal('');
  protected readonly generated = signal('');
  protected readonly acknowledged = signal(false);

  protected async store(): Promise<void> {
    if (this.busy() || !this.valid()) {
      return;
    }
    this.busy.set(true);
    this.error.set('');
    try {
      const generated = await this.control.setSecret(this.secretName(), this.generate() ? '' : this.value(), this.generate());
      this.value.set('');
      this.again.set('');
      if (generated) {
        this.generated.set(generated);
        this.step.set('stored');
      } else {
        this.finish();
      }
    } catch (error) {
      this.error.set(message(error));
    } finally {
      this.busy.set(false);
    }
  }

  protected async copy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(this.generated());
      this.toasts.success('Copied');
    } catch {
      this.toasts.error('Copying is not possible here, select the text and copy it');
    }
  }


  protected finish(): void {
    this.generated.set('');
    this.done.emit();
  }
}

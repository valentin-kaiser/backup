import { ChangeDetectionStrategy, Component, computed, inject, signal } from '@angular/core';

import { IconComponent } from '../../components/icon/icon.component';
import { SecretInfo } from '../../global/model/backup/api/v1/control';
import { ConfirmService } from '../../global/services/confirm/confirm.service';
import { ControlService, message } from '../../global/services/control/control.service';
import { ToastService } from '../../global/services/toast/toast.service';
import { formatRelative, formatTime } from '../../global/utils/format';

const NAME = /^[a-z0-9][a-z0-9_-]{0,31}$/;

type Mode = 'set' | 'change';

/** What the admin is doing with an existing secret. */
interface Action {
  name: string;
  mode: Mode;
}

/** A value that was generated for the admin. It is only shown now. */
interface Generated {
  name: string;
  value: string;
  what: string;
}

/** The stored repository passwords of the daemon. Values are never shown, only generated ones once. */
@Component({
  selector: 'app-secrets',
  imports: [IconComponent],
  template: `
    <header class="page-header">
      <div>
        <h1>Secrets</h1>
        <p class="muted">
          The repository passwords, kept encrypted on the daemon. A job picks one in its advanced
          settings. Values are never shown again, and a lost password means the backups cannot be read.
        </p>
      </div>
    </header>

    @if (control.error()) {
      <div class="banner error" role="alert"><app-icon name="warning" /> {{ control.error() }}</div>
    }
    @if (error()) {
      <div class="banner error" role="alert"><app-icon name="warning" /> {{ error() }}</div>
    }

    @if (generated(); as g) {
      <div class="banner warn" role="status">
        <app-icon name="warning" />
        <div>
          The {{ g.what }} of <strong>{{ g.name }}</strong> was generated. This is the only time it is shown, store it in your password
          manager now. Without it the repository cannot be restored.
          <pre class="mono secret-value">{{ g.value }}</pre>
          <button type="button" class="ghost small" (click)="copy(g.value)">Copy</button>
          <button type="button" class="primary small" (click)="generated.set(null)">I have stored it</button>
        </div>
      </div>
    }
    @if (warnings().length) {
      <div class="banner warn" role="status">
        <app-icon name="warning" />
        <ul>
          @for (w of warnings(); track w) {
            <li>{{ w }}</li>
          }
        </ul>
      </div>
    }

    <section>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Changed</th>
              <th>Used by</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            @for (s of control.secrets(); track s.name) {
              <tr>
                <td class="strong">{{ s.name }}</td>
                <td class="muted" [title]="formatTime(s.updateTime)">{{ formatRelative(s.updateTime) }}</td>
                <td>{{ users(s) }}</td>
                <td class="right">
                  <button type="button" class="ghost small" (click)="start(s, 'set')">Set value</button>
                  <button type="button" class="ghost small" [disabled]="!inUse(s)" (click)="start(s, 'change')">Change password</button>
                  <button type="button" class="icon danger-text" [attr.aria-label]="'Delete ' + s.name" [disabled]="inUse(s)" (click)="remove(s)">
                    <app-icon name="delete" />
                  </button>
                </td>
              </tr>
              @if (action()?.name === s.name) {
                <tr>
                  <td colspan="4">
                    <form class="form" (submit)="$event.preventDefault(); submit()" novalidate>
                      @if (action()?.mode === 'set') {
                        <p class="muted">
                          <strong>Set value</strong> only changes what the daemon presents to restic. It does not change any repository.
                          Use it to enter the password of a repository the daemon cannot open, not to change a password.
                        </p>
                      } @else {
                        <p class="muted">
                          <strong>Change password</strong> adds the new password to {{ users(s) }}, stores it, and removes the old
                          one. If anything fails, the old password keeps working. The repositories must be reachable now.
                        </p>
                      }
                      <label class="check">
                        <input #gen type="checkbox" [checked]="generate()" (change)="generate.set(gen.checked)" />
                        <span><strong>Generate a strong password</strong><span class="hint">It is shown once.</span></span>
                      </label>
                      @if (!generate()) {
                        <div class="grid">
                          <div class="field">
                            <label for="v1">New password</label>
                            <input #v1 id="v1" type="password" autocomplete="new-password" [value]="value()" (input)="value.set(v1.value)" />
                          </div>
                          <div class="field">
                            <label for="v2">Repeat</label>
                            <input #v2 id="v2" type="password" autocomplete="new-password" [value]="again()" (input)="again.set(v2.value)" />
                            @if (again() && !matches()) {
                              <span class="field-error">The passwords differ.</span>
                            }
                          </div>
                        </div>
                      }
                      <div class="form-actions">
                        <button type="button" class="ghost" (click)="action.set(null)">Cancel</button>
                        <button type="submit" class="primary" [disabled]="busy() || !actionValid()">
                          {{ busy() ? 'Working…' : action()?.mode === 'set' ? 'Store' : 'Change password' }}
                        </button>
                      </div>
                    </form>
                  </td>
                </tr>
              }
            } @empty {
              <tr>
                <td colspan="4" class="muted">No secrets yet. Jobs use their password file until one is picked.</td>
              </tr>
            }
          </tbody>
        </table>
      </div>
    </section>

    <form class="form" (submit)="$event.preventDefault(); create()" novalidate>
      <section class="card">
        <h2>Add a secret</h2>
        <div class="field">
          <label for="n-name">Name</label>
          <input #name id="n-name" autocomplete="off" maxlength="32" placeholder="photos" [value]="newName()" (input)="newName.set(name.value)" />
          @if (newName() && !validName()) {
            <span class="field-error">Lower case letters, digits, '-' and '_', at most 32.</span>
          }
        </div>
        <label class="check">
          <input #ngen type="checkbox" [checked]="newGenerate()" (change)="newGenerate.set(ngen.checked)" />
          <span>
            <strong>Generate a strong password</strong>
            <span class="hint">For a repository that does not exist yet. It is shown once.</span>
          </span>
        </label>
        @if (!newGenerate()) {
          <div class="field">
            <label for="n-value">Password of the repository</label>
            <input #nval id="n-value" type="password" autocomplete="new-password" [value]="newValue()" (input)="newValue.set(nval.value)" />
            <span class="hint">A single line. Adding a secret does not change any repository.</span>
          </div>
        }
        <div class="form-actions">
          <button type="submit" class="primary" [disabled]="busy() || !validName() || (!newGenerate() && !newValue())">
            <app-icon name="add" /> Add secret
          </button>
        </div>
      </section>
    </form>
  `,
  styles: `
    .table-wrap {
      border: 0;
      border-radius: 0;
      background: none;
      box-shadow: none;
    }
    .secret-value {
      display: block;
      padding: 0.5rem 0.75rem;
      margin: 0.5rem 0;
      user-select: all;
      word-break: break-all;
      white-space: pre-wrap;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class SecretsPage {
  protected readonly control = inject(ControlService);
  private readonly toasts = inject(ToastService);
  private readonly confirm = inject(ConfirmService);

  protected readonly formatTime = formatTime;
  protected readonly formatRelative = formatRelative;

  protected readonly error = signal('');
  protected readonly busy = signal(false);
  protected readonly generated = signal<Generated | null>(null);
  protected readonly warnings = signal<string[]>([]);

  protected readonly action = signal<Action | null>(null);
  protected readonly generate = signal(false);
  protected readonly value = signal('');
  protected readonly again = signal('');
  protected readonly matches = computed(() => this.value() === this.again());
  protected readonly actionValid = computed(() => this.generate() || (!!this.value() && this.matches()));

  protected readonly newName = signal('');
  protected readonly newGenerate = signal(true);
  protected readonly newValue = signal('');

  constructor() {
    void this.control.loadSecrets();
  }

  protected validName(): boolean {
    return NAME.test(this.newName());
  }

  protected inUse(s: SecretInfo): boolean {
    return s.jobs.length > 0 || s.defaultPassword;
  }

  protected users(s: SecretInfo): string {
    const names = [...s.jobs, ...(s.defaultPassword ? ['the default password'] : [])];
    return names.length ? names.join(', ') : 'nothing yet';
  }

  protected start(s: SecretInfo, mode: Mode): void {
    this.action.set({ name: s.name, mode });
    this.generate.set(false);
    this.value.set('');
    this.again.set('');
  }

  protected async copy(value: string): Promise<void> {
    try {
      await navigator.clipboard.writeText(value);
      this.toasts.success('Copied');
    } catch {
      this.toasts.error('Copying is not possible here, select the text and copy it');
    }
  }

  protected async create(): Promise<void> {
    if (this.busy() || !this.validName()) {
      return;
    }
    const name = this.newName();
    this.busy.set(true);
    this.error.set('');
    try {
      const generated = await this.control.setSecret(name, this.newGenerate() ? '' : this.newValue(), this.newGenerate());
      this.toasts.success(`Stored ${name}`);
      if (generated) {
        this.generated.set({ name, value: generated, what: 'password' });
      }
      this.newName.set('');
      this.newValue.set('');
    } catch (error) {
      this.error.set(message(error));
    } finally {
      this.busy.set(false);
    }
  }

  protected async submit(): Promise<void> {
    const action = this.action();
    if (!action || this.busy() || !this.actionValid()) {
      return;
    }
    if (action.mode === 'change') {
      const ok = await this.confirm.ask({
        title: `Change the password of ${action.name}?`,
        message:
          'The repositories that use it get the new password. If you generated it, it is shown once afterwards. This can take a while.',
        action: 'Change password',
      });
      if (!ok) {
        return;
      }
    }
    this.busy.set(true);
    this.error.set('');
    this.warnings.set([]);
    try {
      let generated = '';
      if (action.mode === 'set') {
        generated = await this.control.setSecret(action.name, this.value(), this.generate());
        this.toasts.success(`Stored ${action.name}`);
      } else {
        const res = await this.control.changeRepositoryPassword(action.name, this.value(), this.generate());
        generated = res.generatedValue;
        this.warnings.set(res.warnings);
        this.toasts.success(`Changed the password of ${res.repositories} ${res.repositories === 1 ? 'repository' : 'repositories'}`);
      }
      if (generated) {
        this.generated.set({ name: action.name, value: generated, what: 'new password' });
      }
      this.action.set(null);
      this.value.set('');
      this.again.set('');
    } catch (error) {
      this.error.set(message(error));
    } finally {
      this.busy.set(false);
    }
  }

  protected async remove(s: SecretInfo): Promise<void> {
    const confirmed = await this.confirm.ask({
      title: `Delete ${s.name}?`,
      message: 'The stored password is gone. If it is the only copy of a repository password, that repository can no longer be opened.',
      action: 'Delete',
      danger: true,
    });
    if (!confirmed) {
      return;
    }
    try {
      await this.control.deleteSecret(s.name);
    } catch (error) {
      this.toasts.error(message(error));
    }
  }
}

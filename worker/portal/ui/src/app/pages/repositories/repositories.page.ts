import { ChangeDetectionStrategy, Component, computed, inject, signal } from '@angular/core';
import { RouterLink } from '@angular/router';

import { IconComponent } from '../../components/icon/icon.component';
import { Repository } from '../../global/model/backup/api/v1/control';
import { ConfirmService } from '../../global/services/confirm/confirm.service';
import { ControlService, message } from '../../global/services/control/control.service';

const NAME = /^[a-z0-9][a-z0-9_-]{0,31}$/;

function blankRepository(): Repository {
  return Repository.create({ restoreRoot: '/restore' });
}

/**
 * The repositories the jobs work on. A repository has its own password, mount check and restore directory, the backup,
 * forget and check jobs that use it share them.
 */
@Component({
  selector: 'app-repositories',
  imports: [IconComponent, RouterLink],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <header class="page-header">
      <div>
        <h1>Repositories</h1>
        <p class="muted">
          Each repository has its own password, its own mount check and its own restore directory. Jobs choose the repository they
          work on, so the backups of different volumes never share settings by accident.
        </p>
      </div>
    </header>

    @if (error()) {
      <div class="banner error" role="alert"><app-icon name="warning" /> {{ error() }}</div>
    }

    <section class="card">
      <div class="section-header">
        <h2>Repositories</h2>
        <button type="button" class="primary small" (click)="add()"><app-icon name="add" /> Add repository</button>
      </div>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Location</th>
              <th>Password</th>
              <th>Mount check</th>
              <th>Jobs</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            @for (r of control.repositories(); track r.name) {
              <tr>
                <td class="strong">{{ r.name }}</td>
                <td class="mono">{{ r.path }}</td>
                <td>
                  @if (r.passwordSecret) {
                    <span class="mono">{{ r.passwordSecret }}</span>
                    @if (!secretKnown(r.passwordSecret)) {
                      <small class="danger-text"> missing</small>
                    }
                  } @else {
                    <span class="muted">password file</span>
                  }
                </td>
                <td class="mono">{{ r.mountPath ? r.mountPath + (r.mountMarker ? '/' + r.mountMarker : '') : '–' }}</td>
                <td>{{ r.jobs.length ? r.jobs.join(', ') : '–' }}</td>
                <td class="right">
                  @if (!standalone(r)) {
                    <button type="button" class="ghost small" (click)="edit(r)"><app-icon name="edit" /> Edit</button>
                    <button type="button" class="icon danger-text" [attr.aria-label]="'Remove ' + r.name" (click)="remove(r)">
                      <app-icon name="delete" />
                    </button>
                  }
                </td>
              </tr>
            } @empty {
              <tr>
                <td colspan="6" class="muted">No repositories yet. Add one, then create the jobs that back up to it.</td>
              </tr>
            }
          </tbody>
        </table>
      </div>

      @if (form(); as f) {
        <form class="form" (submit)="$event.preventDefault(); save()" novalidate>
          <h3>{{ editing() ? 'Edit ' + f.name : 'New repository' }}</h3>
          <div class="grid">
            <div class="field">
              <label for="r-name">Name</label>
              <input
                #rn
                id="r-name"
                [value]="f.name"
                [disabled]="editing()"
                maxlength="32"
                autocomplete="off"
                placeholder="documents"
                (input)="setForm({ name: rn.value })"
              />
              @if (submitted() && !nameOk()) {
                <span class="field-error">Use 1-32 lower case letters, digits, - or _</span>
              } @else if (submitted() && !editing() && nameTaken()) {
                <span class="field-error">This name is already taken</span>
              } @else {
                <span class="hint">Jobs refer to the repository by this name.</span>
              }
            </div>
            <div class="field">
              <label for="r-path">Location</label>
              <input
                #rp
                id="r-path"
                class="mono"
                [value]="f.path"
                autocomplete="off"
                placeholder="/backup/storage/documents"
                (input)="setForm({ path: rp.value })"
              />
              @if (submitted() && !f.path.trim()) {
                <span class="field-error">Enter the location of the repository</span>
              } @else {
                <span class="hint">A directory, or any other restic repository address. It is created on first use.</span>
              }
            </div>
            <div class="field">
              <label for="r-secret">Password</label>
              <select #rs id="r-secret" (change)="setForm({ passwordSecret: rs.value })">
                <option value="" [selected]="!f.passwordSecret">Choose a secret…</option>
                @for (s of control.secrets(); track s.name) {
                  <option [value]="s.name" [selected]="s.name === f.passwordSecret">{{ s.name }}</option>
                }
              </select>
              @if (submitted() && !f.passwordSecret) {
                <span class="field-error">Pick the secret that holds the password</span>
              } @else {
                <span class="hint">
                  A password kept encrypted on the daemon. Create it first under <a routerLink="/secrets">Secrets</a>.
                </span>
              }
            </div>
            <div class="field">
              <label for="r-restore">Restore directory</label>
              <input
                #rr
                id="r-restore"
                class="mono"
                [value]="f.restoreRoot"
                autocomplete="off"
                placeholder="/restore"
                (input)="setForm({ restoreRoot: rr.value })"
              />
              <span class="hint">Restores of this repository are only allowed below this directory.</span>
            </div>
            <div class="field">
              <label for="r-mount">Mount path</label>
              <input
                #rm
                id="r-mount"
                class="mono"
                [value]="f.mountPath"
                autocomplete="off"
                placeholder="/backup/usb"
                (input)="setForm({ mountPath: rm.value })"
              />
              <span class="hint">Jobs of this repository are skipped while this directory is missing. Empty checks nothing.</span>
            </div>
            <div class="field">
              <label for="r-marker">Mount marker</label>
              <input
                #rk
                id="r-marker"
                class="mono"
                [value]="f.mountMarker"
                autocomplete="off"
                placeholder=".mounted"
                (input)="setForm({ mountMarker: rk.value })"
              />
              <span class="hint">A file that exists on the real disk only. Without it an empty mount point passes the check.</span>
            </div>
          </div>

          <div class="form-actions">
            <button type="button" class="ghost" (click)="form.set(null)">Cancel</button>
            <button type="submit" class="primary" [disabled]="busy()">{{ busy() ? 'Saving…' : 'Save repository' }}</button>
          </div>
        </form>
      }
    </section>
  `,
})
export class RepositoriesPage {
  protected readonly control = inject(ControlService);
  private readonly confirm = inject(ConfirmService);

  protected readonly form = signal<Repository | null>(null);
  protected readonly editing = signal(false);
  protected readonly submitted = signal(false);
  protected readonly busy = signal(false);
  protected readonly error = signal('');

  protected readonly nameOk = computed(() => NAME.test(this.form()?.name ?? ''));
  protected readonly nameTaken = computed(() => this.control.repositories().some((r) => r.name === this.form()?.name));

  constructor() {
    void this.control.loadSecrets();
  }

  protected secretKnown(name: string): boolean {
    return this.control.secrets().some((s) => s.name === name);
  }

  /** The implicit repository of a daemon in standalone mode has no stored password and cannot be edited. */
  protected standalone(repository: Repository): boolean {
    return !repository.passwordSecret;
  }

  protected add(): void {
    this.editing.set(false);
    this.submitted.set(false);
    this.form.set(blankRepository());
  }

  protected edit(repository: Repository): void {
    this.editing.set(true);
    this.submitted.set(false);
    this.form.set({ ...repository });
  }

  protected setForm(change: Partial<Repository>): void {
    this.form.update((f) => (f ? { ...f, ...change } : f));
  }

  protected async save(): Promise<void> {
    const f = this.form();
    this.submitted.set(true);
    this.error.set('');
    if (!f || !this.nameOk() || (!this.editing() && this.nameTaken()) || !f.path.trim() || !f.passwordSecret) {
      return;
    }
    this.busy.set(true);
    try {
      await this.control.saveRepository(f);
      this.form.set(null);
    } catch (e) {
      this.error.set(message(e));
    } finally {
      this.busy.set(false);
    }
  }

  protected async remove(repository: Repository): Promise<void> {
    const confirmed = await this.confirm.ask({
      title: `Remove ${repository.name}?`,
      message: repository.jobs.length
        ? `The jobs ${repository.jobs.join(', ')} work on it, change or delete them first.`
        : 'Only the entry is removed. The backups stay at their location and can be added again.',
      action: 'Remove',
      danger: true,
    });
    if (!confirmed) {
      return;
    }
    try {
      await this.control.deleteRepository(repository.name);
    } catch (e) {
      this.error.set(message(e));
    }
  }
}

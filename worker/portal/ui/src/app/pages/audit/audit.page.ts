import { ChangeDetectionStrategy, Component, computed, effect, inject, signal, untracked } from '@angular/core';

import { IconComponent } from '../../components/icon/icon.component';
import { AuditEntry, ExportAuditLogResponse, ListAuditLogResponse } from '../../global/model/backup/api/v1/audit';
import { api } from '../../global/utils/api';
import { message } from '../../global/services/control/control.service';

/** Actions to filter by, with the words shown for them. */
export const ACTIONS: { value: string; label: string }[] = [
  { value: 'login', label: 'Sign-in' },
  { value: 'login_failed', label: 'Failed sign-in' },
  { value: 'logout', label: 'Sign-out' },
  { value: 'start_job', label: 'Run job' },
  { value: 'cancel_job', label: 'Cancel job' },
  { value: 'pause_job', label: 'Pause job' },
  { value: 'resume_job', label: 'Resume job' },
  { value: 'create_job', label: 'Create job' },
  { value: 'update_job', label: 'Change job' },
  { value: 'delete_job', label: 'Delete job' },
  { value: 'restore', label: 'Restore' },
  { value: 'set_secret', label: 'Set secret' },
  { value: 'change_repository_password', label: 'Change repository password' },
  { value: 'delete_secret', label: 'Delete secret' },
  { value: 'call_refused', label: 'Refused call' },
  { value: 'audit_export', label: 'Log export' },
];

const LABELS = new Map(ACTIONS.map((a) => [a.value, a.label]));
const PAGE = 100;
const DAY_MS = 24 * 60 * 60 * 1000;
const SEARCH_DELAY_MS = 300;

/** The audit log: what was done through the portal, to what, from where and whether it worked. */
@Component({
  selector: 'app-audit',
  imports: [IconComponent],
  template: `
    <header class="page-header">
      <div>
        <h1>Audit log</h1>
        <p class="muted">
          Sign-ins and everything that changes something: jobs, restores and secrets. The portal has one password,
          so an entry names the address it came from, not a person. Reading is not recorded, and neither are secrets, only their names.
        </p>
      </div>
    </header>

    @if (error()) {
      <div class="banner error" role="alert"><app-icon name="alert" /> {{ error() }}</div>
    }

    <section class="card">
      <div class="toolbar">
        <div class="field inline">
          <label for="a-action">Action</label>
          <select id="a-action" (change)="action.set($any($event.target).value)">
            <option value="">All</option>
            @for (a of actions; track a.value) {
              <option [value]="a.value">{{ a.label }}</option>
            }
          </select>
        </div>
        <div class="field inline">
          <label for="a-result">Result</label>
          <select id="a-result" (change)="result.set($any($event.target).value)">
            <option value="">All</option>
            <option value="ok">Worked</option>
            <option value="failed">Failed</option>
            <option value="denied">Denied</option>
            <option value="unknown">Unknown</option>
          </select>
        </div>
        <div class="field inline">
          <label for="a-since">From</label>
          <input id="a-since" type="date" (change)="from.set($any($event.target).value)" />
        </div>
        <div class="field inline">
          <label for="a-until">To</label>
          <input id="a-until" type="date" (change)="to.set($any($event.target).value)" />
        </div>
        <div class="field inline grow">
          <label for="a-q">Search</label>
          <input #q id="a-q" type="search" placeholder="job, secret, text…" autocomplete="off" (input)="typed(q.value)" />
        </div>
      </div>

      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Time</th>
              <th>Action</th>
              <th>Target</th>
              <th>Result</th>
              <th>From</th>
            </tr>
          </thead>
          <tbody>
            @for (e of entries(); track e.id) {
              <tr>
                <td [title]="e.time?.toISOString()">{{ when(e.time) }}</td>
                <td class="strong">{{ label(e.action) }}</td>
                <td>
                  {{ e.target || '-' }}
                  @if (e.detail) {
                    <div class="muted detail">{{ e.detail }}</div>
                  }
                </td>
                <td>
                  <span class="chip" [class]="chip(e.result)"><span class="dot"></span> {{ resultLabel(e.result) }}</span>
                </td>
                <td class="muted mono">{{ e.ip }}</td>
              </tr>
            } @empty {
              <tr>
                <td colspan="6" class="muted">{{ loading() ? 'Loading…' : 'Nothing recorded for these filters.' }}</td>
              </tr>
            }
          </tbody>
        </table>
      </div>

      <div class="form-actions">
        @if (next()) {
          <button type="button" class="secondary" [disabled]="loading()" (click)="more()">Load older entries</button>
        }
        <button type="button" class="ghost" (click)="download('csv')"><app-icon name="file" /> Export CSV</button>
        <button type="button" class="ghost" (click)="download('json')">Export JSON</button>
      </div>
    </section>
  `,
  styles: `
    .detail {
      font-size: 0.85em;
      word-break: break-word;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class AuditPage {
  protected readonly actions = ACTIONS;

  protected readonly action = signal('');
  protected readonly result = signal('');
  protected readonly from = signal('');
  protected readonly to = signal('');
  protected readonly text = signal('');

  protected readonly entries = signal<AuditEntry[]>([]);
  protected readonly next = signal(0);
  protected readonly loading = signal(false);
  protected readonly error = signal('');

  private searchTimer: ReturnType<typeof setTimeout> | undefined;
  /** Ignores the answer of a request that a newer one replaced. */
  private generation = 0;

  /** The audit filter of the current inputs. */
  private readonly filter = computed(() => ({
    action: this.action(),
    result: this.result(),
    text: this.text(),
    since: this.from() ? new Date(this.from() + 'T00:00:00') : undefined,
    // A date includes that whole day.
    until: this.to() ? new Date(new Date(this.to() + 'T00:00:00').getTime() + DAY_MS) : undefined,
  }));

  constructor() {
    // Every change of a filter loads the first page again.
    effect(() => {
      this.filter();
      untracked(() => void this.load(false));
    });
  }

  protected typed(value: string): void {
    clearTimeout(this.searchTimer);
    this.searchTimer = setTimeout(() => this.text.set(value.trim()), SEARCH_DELAY_MS);
  }

  protected more(): void {
    void this.load(true);
  }

  private async load(append: boolean): Promise<void> {
    const generation = ++this.generation;
    const beforeId = append ? this.next() : 0;
    this.loading.set(true);
    try {
      const page = await api('AuditLog', 'ListAuditLog', { filter: this.filter(), beforeId, limit: PAGE }, ListAuditLogResponse);
      if (generation !== this.generation) {
        return;
      }
      this.entries.update((old) => (append ? [...old, ...page.entries] : page.entries));
      this.next.set(page.nextId);
      this.error.set('');
    } catch (error) {
      if (generation === this.generation) {
        this.error.set(message(error));
      }
    } finally {
      if (generation === this.generation) {
        this.loading.set(false);
      }
    }
  }

  /** Downloads everything that matches the filters, not only the page on screen. */
  protected async download(format: 'csv' | 'json'): Promise<void> {
    try {
      const file = await api('AuditLog', 'ExportAuditLog', { filter: this.filter(), format }, ExportAuditLogResponse);
      const url = URL.createObjectURL(new Blob([file.data as BlobPart], { type: file.contentType }));
      const link = document.createElement('a');
      link.href = url;
      link.download = file.filename;
      link.click();
      URL.revokeObjectURL(url);
    } catch (error) {
      this.error.set(message(error));
    }
  }

  protected label(action: string): string {
    return LABELS.get(action) ?? action;
  }

  protected when(time: Date | undefined): string {
    return time ? time.toLocaleString() : '';
  }

  protected chip(result: string): string {
    return { ok: 'success', failed: 'failed', denied: 'failed', unknown: 'cancelled' }[result] ?? '';
  }

  protected resultLabel(result: string): string {
    return { ok: 'Worked', failed: 'Failed', denied: 'Denied', unknown: 'Unknown' }[result] ?? result;
  }
}

import { ChangeDetectionStrategy, Component, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { Router } from '@angular/router';

import { IconComponent } from '../../components/icon/icon.component';
import { RunTableComponent } from '../../components/run-table/run-table.component';
import { Run } from '../../global/model/backup/api/v1/control';
import { ControlService, message } from '../../global/services/control/control.service';
import { statusName } from '../../global/utils/format';

const PAGE = 50;
const MAX = 500;
const STATUSES = ['success', 'failed', 'running', 'queued', 'cancelled', 'skipped'];

/** The run history, filtered by job in the URL so views can be linked. It reloads when runs change. */
@Component({
  selector: 'app-history',
  imports: [IconComponent, RunTableComponent],
  templateUrl: './history.page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class HistoryPage {
  protected readonly control = inject(ControlService);
  private readonly router = inject(Router);

  /** Job filter from the query string. */
  readonly job = input<string | undefined>(undefined);

  protected readonly statuses = STATUSES;
  protected readonly status = signal('');
  protected readonly limit = signal(PAGE);
  protected readonly runs = signal<Run[] | null>(null);
  protected readonly error = signal('');
  protected readonly loading = signal(false);
  private request = 0;

  protected readonly shown = computed(() => {
    const status = this.status();
    const runs = this.runs() ?? [];
    return status ? runs.filter((run) => statusName(run.status) === status) : runs;
  });
  /** There may be more when the server returned a full page. */
  protected readonly more = computed(() => (this.runs()?.length ?? 0) >= this.limit() && this.limit() < MAX);

  constructor() {
    effect(() => {
      const job = this.job() ?? '';
      const limit = this.limit();
      this.control.changes();
      untracked(() => void this.load(job, limit));
    });
  }

  private async load(job: string, limit: number): Promise<void> {
    const request = ++this.request;
    this.loading.set(true);
    try {
      const runs = await this.control.loadRuns(job, limit);
      if (request === this.request) {
        this.runs.set(runs);
        this.error.set('');
      }
    } catch (error) {
      if (request === this.request) {
        this.error.set(message(error));
      }
    } finally {
      if (request === this.request) {
        this.loading.set(false);
      }
    }
  }

  protected chooseJob(job: string): void {
    this.limit.set(PAGE);
    void this.router.navigate([], { queryParams: { job: job || null }, queryParamsHandling: 'merge' });
  }

  protected showMore(): void {
    this.limit.update((limit) => Math.min(MAX, limit + PAGE));
  }
}

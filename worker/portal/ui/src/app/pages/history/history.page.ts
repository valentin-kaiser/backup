import { ChangeDetectionStrategy, Component, computed, effect, inject, input, signal, untracked } from '@angular/core';

import { IconComponent } from '../../components/icon/icon.component';
import { RunTableComponent } from '../../components/run-table/run-table.component';
import { Run, RunStatus } from '../../global/model/backup/api/v1/control';
import { ControlService, message } from '../../global/services/control/control.service';
import { formatRelative, formatTime, statusName } from '../../global/utils/format';

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
  protected readonly formatRelative = formatRelative;
  protected readonly formatTime = formatTime;

  protected readonly enabled = computed(() => this.control.definedJobs().filter((job) => job.enabled && !job.paused).length);
  protected readonly failures = computed(
    () => this.control.runs().filter((run) => run.status === RunStatus.RUN_STATUS_FAILED).length,
  );

  /** The newest successful backup of any backup job. */
  protected readonly lastBackup = computed(() => {
    const times = this.control
      .definedJobs()
      .filter((job) => job.type === 'backup' && job.lastSuccessTime)
      .map((job) => job.lastSuccessTime as Date);
    return times.length ? new Date(Math.max(...times.map((t) => t.getTime()))) : undefined;
  });

  /** Job filter from the query string. */
  readonly job = input<string | undefined>(undefined);
  /** The filter picked in the page, it wins over the one from the query string. */
  protected readonly chosen = signal<string | undefined>(undefined);
  protected readonly selectedJob = computed(() => this.chosen() ?? this.job() ?? '');

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
      const job = this.selectedJob();
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
    this.chosen.set(job);
  }

  protected showMore(): void {
    this.limit.update((limit) => Math.min(MAX, limit + PAGE));
  }
}

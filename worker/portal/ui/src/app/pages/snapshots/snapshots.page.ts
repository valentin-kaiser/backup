import {
  ChangeDetectionStrategy,
  Component,
  DestroyRef,
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
  viewChild,
} from '@angular/core';
import { RouterLink } from '@angular/router';

import { IconComponent } from '../../components/icon/icon.component';
import { RestoreDialogComponent } from '../../components/restore-dialog/restore-dialog.component';
import { RepositoryStats, Snapshot } from '../../global/model/backup/api/v1/control';
import { ControlService, Repository, message } from '../../global/services/control/control.service';
import { formatBytes, formatRelative, formatTime } from '../../global/utils/format';

/** Nothing else changes a repository, a slow poll only catches changes made by other tools. */
const REFRESH_MS = 5 * 60_000;

/**
 * The snapshots of the repositories the jobs work on. They load when the page opens, after every backup
 * or prune and now and then, there is no button for it.
 */
@Component({
  selector: 'app-snapshots',
  imports: [RouterLink, IconComponent, RestoreDialogComponent],
  templateUrl: './snapshots.page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class SnapshotsPage {
  protected readonly control = inject(ControlService);
  private readonly restoreDialog = viewChild.required(RestoreDialogComponent);

  protected readonly formatBytes = formatBytes;
  protected readonly formatTime = formatTime;
  protected readonly formatRelative = formatRelative;

  /** A job of the repository to show, from the query string. */
  readonly job = input<string | undefined>(undefined);
  private readonly chosen = signal<string | undefined>(undefined);

  protected readonly snapshots = signal<Snapshot[] | null>(null);
  protected readonly stats = signal<RepositoryStats | null>(null);
  protected readonly error = signal('');
  protected readonly loading = signal(false);
  protected readonly filter = signal('');
  private request = 0;
  /** Repositories whose statistics are being scanned. A scan reads the whole repository and can outlast a refresh. */
  private readonly scanning = new Set<string>();

  /** The repository in view: the one of the job in the URL, else the first. */
  protected readonly selected = computed<Repository | undefined>(() => {
    const repositories = this.control.repositories();
    const job = this.chosen() ?? this.job();
    return repositories.find((repo) => job && repo.jobs.includes(job)) ?? repositories[0];
  });

  protected readonly shown = computed(() => {
    const term = this.filter().trim().toLowerCase();
    const snapshots = this.snapshots() ?? [];
    if (!term) {
      return snapshots;
    }
    return snapshots.filter((s) =>
      [s.id, s.hostname, s.username, ...s.paths, ...s.tags].some((value) => value.toLowerCase().includes(term)),
    );
  });

  constructor() {
    effect(() => {
      const repository = this.selected();
      this.control.repositoryChanges();
      untracked(() => void this.load(repository));
    });
    const timer = setInterval(() => void this.load(untracked(() => this.selected())), REFRESH_MS);
    inject(DestroyRef).onDestroy(() => clearInterval(timer));
  }

  private async load(repository: Repository | undefined): Promise<void> {
    if (!repository) {
      return;
    }
    const request = ++this.request;
    this.loading.set(true);
    try {
      // The list comes first, the statistics scan the whole repository and can take long.
      const snapshots = await this.control.loadSnapshots(repository.job);
      if (request === this.request) {
        this.snapshots.set(snapshots);
        this.error.set('');
        this.loading.set(false);
      }
      await this.scan(repository);
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

  /** Scans the statistics of a repository, unless a scan of it is already running, then that one delivers. */
  private async scan(repository: Repository): Promise<void> {
    if (this.scanning.has(repository.key)) {
      return;
    }
    this.scanning.add(repository.key);
    try {
      const stats = await this.control.loadStats(repository.job).catch(() => null);
      if (this.selected()?.key === repository.key) {
        this.stats.set(stats);
      }
    } finally {
      this.scanning.delete(repository.key);
    }
  }

  protected choose(repository: Repository): void {
    if (repository.key === this.selected()?.key) {
      return;
    }
    this.snapshots.set(null);
    this.stats.set(null);
    this.error.set('');
    this.chosen.set(repository.job);
  }

  protected reload(): void {
    void this.load(this.selected());
  }

  protected restore(snapshot: Snapshot): void {
    const repository = this.selected();
    if (repository) {
      this.restoreDialog().open(repository.job, snapshot);
    }
  }
}

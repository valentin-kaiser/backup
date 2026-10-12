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
import { Router, RouterLink } from '@angular/router';

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
  private readonly router = inject(Router);
  private readonly restoreDialog = viewChild.required(RestoreDialogComponent);

  protected readonly formatBytes = formatBytes;
  protected readonly formatTime = formatTime;
  protected readonly formatRelative = formatRelative;

  /** A job of the repository to show, from the query string. */
  readonly job = input<string | undefined>(undefined);

  protected readonly snapshots = signal<Snapshot[] | null>(null);
  protected readonly stats = signal<RepositoryStats | null>(null);
  protected readonly error = signal('');
  protected readonly loading = signal(false);
  protected readonly filter = signal('');
  private request = 0;

  /** The repository in view: the one of the job in the URL, else the first. */
  protected readonly selected = computed<Repository | undefined>(() => {
    const repositories = this.control.repositories();
    const job = this.job();
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
      const [snapshots, stats] = await Promise.all([
        this.control.loadSnapshots(repository.job),
        this.control.loadStats(repository.job).catch(() => null),
      ]);
      if (request === this.request) {
        this.snapshots.set(snapshots);
        this.stats.set(stats);
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

  protected choose(repository: Repository): void {
    if (repository.key === this.selected()?.key) {
      return;
    }
    this.snapshots.set(null);
    this.stats.set(null);
    this.error.set('');
    void this.router.navigate([], { queryParams: { job: repository.job }, queryParamsHandling: 'merge' });
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

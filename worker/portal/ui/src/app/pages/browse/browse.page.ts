import { ChangeDetectionStrategy, Component, computed, effect, inject, input, signal, untracked, viewChild } from '@angular/core';
import { Router, RouterLink } from '@angular/router';

import { IconComponent } from '../../components/icon/icon.component';
import { RestoreDialogComponent } from '../../components/restore-dialog/restore-dialog.component';
import { FileNode, FileType, Snapshot } from '../../global/model/backup/api/v1/control';
import { ControlService, message } from '../../global/services/control/control.service';
import { formatBytes, formatRelative, formatTime } from '../../global/utils/format';

export interface Crumb {
  name: string;
  path: string;
}

/** The directory path as links: "/a/b" is root, "a" and "b". */
export function breadcrumbs(path: string): Crumb[] {
  const crumbs: Crumb[] = [{ name: '/', path: '/' }];
  let current = '';
  for (const part of path.split('/').filter(Boolean)) {
    current += `/${part}`;
    crumbs.push({ name: part, path: current });
  }
  return crumbs;
}

/** The files of one snapshot. A directory is read when it is opened, a snapshot never changes. */
@Component({
  selector: 'app-browse',
  imports: [RouterLink, IconComponent, RestoreDialogComponent],
  templateUrl: './browse.page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class BrowsePage {
  private readonly control = inject(ControlService);
  private readonly router = inject(Router);
  private readonly restoreDialog = viewChild.required(RestoreDialogComponent);

  protected readonly formatBytes = formatBytes;
  protected readonly formatTime = formatTime;
  protected readonly formatRelative = formatRelative;
  protected readonly FileType = FileType;

  /** The snapshot id from the route. */
  readonly snapshot = input.required<string>();
  /** A job of the repository, from the query string. */
  readonly job = input<string | undefined>(undefined);
  /** The directory, from the query string. */
  readonly path = input<string | undefined>(undefined);

  protected readonly files = signal<FileNode[] | null>(null);
  protected readonly truncated = signal(false);
  protected readonly details = signal<Snapshot | null>(null);
  protected readonly error = signal('');
  protected readonly loading = signal(false);
  protected readonly filter = signal('');
  private request = 0;

  protected readonly directory = computed(() => `/${(this.path() ?? '').split('/').filter(Boolean).join('/')}`);
  protected readonly crumbs = computed(() => breadcrumbs(this.directory()));
  protected readonly shortId = computed(() => this.snapshot().slice(0, 8));
  protected readonly shown = computed(() => {
    const term = this.filter().trim().toLowerCase();
    const files = this.files() ?? [];
    return term ? files.filter((file) => file.name.toLowerCase().includes(term)) : files;
  });
  protected readonly parent = computed(() => this.crumbs().at(-2)?.path);

  constructor() {
    effect(() => {
      const job = this.job() ?? '';
      const snapshot = this.snapshot();
      const directory = this.directory();
      untracked(() => void this.load(job, snapshot, directory));
    });
    // The header shows when and where the snapshot was taken, the listing does not wait for it.
    effect(() => {
      const job = this.job() ?? '';
      const snapshot = this.snapshot();
      untracked(() => {
        this.control
          .loadSnapshots(job)
          .then((list) => this.details.set(list.find((s) => s.id === snapshot || s.shortId === snapshot) ?? null))
          .catch(() => this.details.set(null));
      });
    });
  }

  private async load(job: string, snapshot: string, directory: string): Promise<void> {
    const request = ++this.request;
    this.loading.set(true);
    this.filter.set('');
    try {
      const response = await this.control.loadFiles(job, snapshot, directory);
      if (request === this.request) {
        this.files.set(response.files);
        this.truncated.set(response.truncated);
        this.error.set('');
      }
    } catch (error) {
      if (request === this.request) {
        this.files.set(null);
        this.error.set(message(error));
      }
    } finally {
      if (request === this.request) {
        this.loading.set(false);
      }
    }
  }

  protected open(path: string): void {
    void this.router.navigate([], { queryParams: { path: path === '/' ? null : path }, queryParamsHandling: 'merge' });
  }

  protected reload(): void {
    void this.load(this.job() ?? '', this.snapshot(), this.directory());
  }

  /** Restores a single entry, or the whole directory that is shown when no entry is given. */
  protected restore(entry?: FileNode): void {
    const snapshot = this.details() ?? Snapshot.create({ id: this.snapshot(), shortId: this.shortId() });
    const path = entry?.path ?? (this.directory() === '/' ? '' : this.directory());
    this.restoreDialog().open(this.job() ?? '', snapshot, path);
  }

  protected backLink(): { link: string[]; params: Record<string, string | undefined> } {
    return { link: ['/'], params: {} };
  }
}

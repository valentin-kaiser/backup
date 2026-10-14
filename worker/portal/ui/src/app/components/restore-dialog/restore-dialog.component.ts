import { ChangeDetectionStrategy, Component, ElementRef, inject, signal, viewChild } from '@angular/core';
import { Router } from '@angular/router';

import { Snapshot } from '../../global/model/backup/api/v1/control';
import { ControlService, message } from '../../global/services/control/control.service';
import { formatTime } from '../../global/utils/format';
import { IconComponent } from '../icon/icon.component';

/** Asks where to restore a snapshot to. The daemon only accepts targets below its restore directory. */
@Component({
  selector: 'app-restore-dialog',
  imports: [IconComponent],
  templateUrl: './restore-dialog.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RestoreDialogComponent {
  private readonly control = inject(ControlService);
  private readonly router = inject(Router);
  private readonly dialog = viewChild.required<ElementRef<HTMLDialogElement>>('dialog');

  protected readonly formatTime = formatTime;
  protected readonly snapshot = signal<Snapshot | null>(null);
  protected readonly target = signal('');
  protected readonly error = signal('');
  protected readonly busy = signal(false);
  /** Paths the restore starts with, e.g. the folder that was being looked at. */
  protected readonly include = signal('');
  private job = '';
  /** Counts the openings of the dialog, an answer that belongs to an earlier one is dropped. */
  private generation = 0;

  /** Restores from the repository that the job works on, everything or only the paths in include. */
  open(job: string, snapshot: Snapshot, include = ''): void {
    this.include.set(include);
    const root = (this.control.defaults()?.restoreRoot || '/restore').replace(/\/+$/, '');
    this.generation++;
    this.busy.set(false);
    this.job = job;
    this.snapshot.set(snapshot);
    this.target.set(`${root}/${snapshot.shortId || snapshot.id.slice(0, 8)}`);
    this.error.set('');
    this.dialog().nativeElement.showModal();
  }

  /** A restore that is being started cannot be cancelled, the dialog stays until the daemon has answered. */
  protected close(): void {
    if (!this.busy()) {
      this.dialog().nativeElement.close();
    }
  }

  /** Escape asks the dialog to cancel itself. */
  protected escape(event: Event): void {
    event.preventDefault();
    this.close();
  }

  protected async submit(include: string): Promise<void> {
    const snapshot = this.snapshot();
    if (!snapshot) {
      return;
    }
    const generation = this.generation;
    this.busy.set(true);
    this.error.set('');
    try {
      const run = await this.control.restore({
        job: this.job,
        snapshot: snapshot.id,
        target: this.target().trim(),
        include: include
          .split('\n')
          .map((path) => path.trim())
          .filter(Boolean),
      });
      if (generation !== this.generation) {
        return;
      }
      this.busy.set(false);
      this.close();
      await this.router.navigate(['/runs', run.id]);
    } catch (error) {
      if (generation === this.generation) {
        this.error.set(message(error));
      }
    } finally {
      if (generation === this.generation) {
        this.busy.set(false);
      }
    }
  }
}

import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core';
import { RouterLink } from '@angular/router';

import { Job } from '../../global/model/backup/api/v1/control';
import { ConfirmService } from '../../global/services/confirm/confirm.service';
import { ControlService } from '../../global/services/control/control.service';
import { describeCron } from '../../global/utils/cron';
import { formatRelative, formatTime, jobTypeLabel } from '../../global/utils/format';
import { IconComponent } from '../icon/icon.component';
import { StatusChipComponent } from '../status-chip/status-chip.component';

/** One job with its state and the actions that make sense right now. */
@Component({
  selector: 'app-job-card',
  imports: [RouterLink, IconComponent, StatusChipComponent],
  templateUrl: './job-card.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class JobCardComponent {
  protected readonly control = inject(ControlService);
  private readonly confirm = inject(ConfirmService);
  protected readonly formatTime = formatTime;
  protected readonly formatRelative = formatRelative;
  protected readonly jobTypeLabel = jobTypeLabel;

  readonly job = input.required<Job>();

  protected readonly status = computed(() => (this.job().active ?? this.job().last)?.status);
  protected readonly schedule = computed(() => {
    const job = this.job();
    return job.enabled ? describeCron(job.schedule) || job.schedule : 'Not scheduled';
  });
  protected readonly lastRun = computed(() => {
    const last = this.job().last;
    return last ? (last.finishTime ?? last.queueTime) : undefined;
  });

  protected async resync(): Promise<void> {
    const job = this.job();
    const confirmed = await this.confirm.ask({
      title: `Resync ${job.name}?`,
      message:
        'The state of both sides is rebuilt from scratch. Files that differ are copied, nothing is deleted, and a file changed on both sides keeps the version selected for conflicts.',
      action: 'Resync',
      danger: false,
    });
    if (confirmed) {
      await this.control.resyncJob(job.name);
    }
  }

  protected async unlock(): Promise<void> {
    const job = this.job();
    const confirmed = await this.confirm.ask({
      title: `Unlock the repository of ${job.name}?`,
      message:
        'All locks of the repository are removed, also those of processes that are still running. Only do this when a lock is left over from a stopped container or process, otherwise a running backup can fail.',
      action: 'Unlock',
      danger: true,
    });
    if (confirmed) {
      await this.control.unlockJob(job.name);
    }
  }

  protected togglePause(): void {
    const job = this.job();
    void (job.paused ? this.control.resumeJob(job.name) : this.control.pauseJob(job.name));
  }
}

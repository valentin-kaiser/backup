import { ChangeDetectionStrategy, Component, inject } from '@angular/core';
import { RouterLink } from '@angular/router';

import { IconComponent } from '../../components/icon/icon.component';
import { StatusChipComponent } from '../../components/status-chip/status-chip.component';
import { Job } from '../../global/model/backup/api/v1/control';
import { ConfirmService } from '../../global/services/confirm/confirm.service';
import { ControlService } from '../../global/services/control/control.service';
import { ToastService } from '../../global/services/toast/toast.service';
import { describeCron } from '../../global/utils/cron';
import { formatRelative, formatTime, jobTypeLabel } from '../../global/utils/format';

@Component({
  selector: 'app-jobs',
  imports: [RouterLink, IconComponent, StatusChipComponent],
  templateUrl: './jobs.page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class JobsPage {
  protected readonly control = inject(ControlService);
  private readonly confirm = inject(ConfirmService);
  private readonly toast = inject(ToastService);

  protected readonly formatRelative = formatRelative;
  protected readonly formatTime = formatTime;
  protected readonly jobTypeLabel = jobTypeLabel;
  protected readonly describeCron = describeCron;

  /** Where a job works, its own setting or the default. */
  protected repository(job: Job): string {
    return job.definition?.repository || this.control.defaults()?.repository || '';
  }

  protected source(job: Job): string {
    if (job.type !== 'backup') {
      return '';
    }
    return job.definition?.source || this.control.defaults()?.source || '';
  }

  protected async remove(job: Job): Promise<void> {
    const confirmed = await this.confirm.ask({
      title: `Delete ${job.name}?`,
      message: 'The job is removed from the configuration and no longer runs. Its history and the snapshots in the repository are kept.',
      action: 'Delete',
      danger: true,
    });
    if (!confirmed) {
      return;
    }
    try {
      await this.control.deleteJob(job.name);
    } catch (error) {
      this.toast.error(error instanceof Error ? error.message : String(error));
    }
  }
}

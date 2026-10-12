import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core';
import { RouterLink } from '@angular/router';

import { Job } from '../../global/model/backup/api/v1/control';
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

  protected togglePause(): void {
    const job = this.job();
    void (job.paused ? this.control.resumeJob(job.name) : this.control.pauseJob(job.name));
  }
}

import {
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  computed,
  effect,
  inject,
  viewChild,
} from '@angular/core';
import { RouterLink } from '@angular/router';

import { IconComponent } from '../../components/icon/icon.component';
import { JobCardComponent } from '../../components/job-card/job-card.component';
import { RunStatus } from '../../global/model/backup/api/v1/control';
import { ControlService } from '../../global/services/control/control.service';
import { formatRelative, formatTime } from '../../global/utils/format';


@Component({
  selector: 'app-dashboard',
  imports: [RouterLink, IconComponent, JobCardComponent],
  templateUrl: './dashboard.page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class DashboardPage {
  protected readonly control = inject(ControlService);
  protected readonly formatRelative = formatRelative;
  protected readonly formatTime = formatTime;
  private readonly output = viewChild<ElementRef<HTMLElement>>('output');

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

  constructor() {
    effect(() => {
      this.control.liveLines();
      const box = this.output()?.nativeElement;
      if (box && box.scrollTop + box.clientHeight >= box.scrollHeight - 24) {
        queueMicrotask(() => (box.scrollTop = box.scrollHeight));
      }
    });
  }
}

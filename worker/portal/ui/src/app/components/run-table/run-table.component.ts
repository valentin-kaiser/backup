import { ChangeDetectionStrategy, Component, input } from '@angular/core';
import { RouterLink } from '@angular/router';

import { Run } from '../../global/model/backup/api/v1/control';
import { formatDuration, formatRelative, formatTime } from '../../global/utils/format';
import { StatusChipComponent } from '../status-chip/status-chip.component';

/** Runs as table rows, each links to the output of the run. */
@Component({
  selector: 'app-run-table',
  imports: [RouterLink, StatusChipComponent],
  templateUrl: './run-table.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RunTableComponent {
  protected readonly formatTime = formatTime;
  protected readonly formatRelative = formatRelative;
  protected readonly formatDuration = formatDuration;

  readonly runs = input.required<Run[]>();
  /** Hide the job column when all runs belong to one job. */
  readonly showJob = input(true);
}

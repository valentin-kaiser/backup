import { ChangeDetectionStrategy, Component, ElementRef, effect, input, signal, viewChild } from '@angular/core';
import { RouterLink } from '@angular/router';

import { Run } from '../../global/model/backup/api/v1/control';
import { formatDuration, formatRelative, formatTime } from '../../global/utils/format';
import { IconComponent } from '../icon/icon.component';
import { StatusChipComponent } from '../status-chip/status-chip.component';

/** Runs as table rows. The details of a run, with its error, open in a drawer. */
@Component({
  selector: 'app-run-table',
  imports: [RouterLink, IconComponent, StatusChipComponent],
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

  protected readonly selected = signal<Run | null>(null);
  private readonly drawer = viewChild.required<ElementRef<HTMLDialogElement>>('drawer');

  constructor() {
    effect(() => {
      const element = this.drawer().nativeElement;
      if (this.selected()) {
        if (!element.open) {
          element.showModal();
        }
      } else if (element.open) {
        element.close();
      }
    });
  }

  /** The run as the list has it now, the drawer follows runs that change while it is open. */
  protected readonly current = () => {
    const selected = this.selected();
    return selected ? (this.runs().find((run) => run.id === selected.id) ?? selected) : null;
  };
}

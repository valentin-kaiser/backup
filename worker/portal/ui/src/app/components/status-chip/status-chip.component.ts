import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';

import { RunStatus } from '../../global/model/backup/api/v1/control';
import { statusName } from '../../global/utils/format';

/** The state of a run as a coloured chip. Without a status the job is idle. */
@Component({
  selector: 'app-status-chip',
  template: `<span class="chip" [class]="name()"><span class="dot"></span>{{ name() }}</span>`,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class StatusChipComponent {
  readonly status = input<RunStatus | undefined>(undefined);
  protected readonly name = computed(() => {
    const status = this.status();
    return status === undefined ? 'idle' : statusName(status);
  });
}

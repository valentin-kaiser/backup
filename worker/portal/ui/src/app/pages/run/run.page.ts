import {
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
  viewChild,
} from '@angular/core';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { RouterLink } from '@angular/router';

import { IconComponent } from '../../components/icon/icon.component';
import { StatusChipComponent } from '../../components/status-chip/status-chip.component';
import { EventType, Run } from '../../global/model/backup/api/v1/control';
import { ControlService, message } from '../../global/services/control/control.service';
import { formatDuration, formatTime, isActive } from '../../global/utils/format';

const MAX_LINES = 5000;

/** The output of one run. While it runs the lines arrive as they are printed. */
@Component({
  selector: 'app-run',
  imports: [RouterLink, IconComponent, StatusChipComponent],
  templateUrl: './run.page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RunPage {
  protected readonly control = inject(ControlService);
  protected readonly formatTime = formatTime;
  private readonly output = viewChild<ElementRef<HTMLElement>>('output');

  readonly id = input.required<string>();

  protected readonly run = signal<Run | null>(null);
  protected readonly lines = signal<string[]>([]);
  protected readonly error = signal('');
  protected readonly active = computed(() => {
    const run = this.run();
    return !!run && isActive(run.status);
  });
  protected readonly duration = computed(() => {
    this.control.changes();
    const run = this.run();
    return run ? formatDuration(run) : '';
  });

  constructor() {
    effect(() => {
      const id = this.id();
      untracked(() => void this.load(id));
    });

    // Lines are appended as they arrive, a change of state loads the run again.
    this.control.events$.pipe(takeUntilDestroyed()).subscribe((event) => {
      if (event.run !== this.id()) {
        return;
      }
      if (event.type === EventType.EVENT_TYPE_LINE) {
        this.lines.update((lines) => [...lines.slice(1 - MAX_LINES), event.line]);
      } else {
        void this.load(this.id());
      }
    });

    effect(() => {
      this.lines();
      const box = this.output()?.nativeElement;
      if (box && box.scrollTop + box.clientHeight >= box.scrollHeight - 24) {
        queueMicrotask(() => (box.scrollTop = box.scrollHeight));
      }
    });
  }

  private async load(id: string): Promise<void> {
    try {
      const run = await this.control.getRun(id);
      if (id === this.id()) {
        this.run.set(run);
        this.lines.set(run.output);
        this.error.set('');
      }
    } catch (error) {
      if (id === this.id()) {
        this.error.set(message(error));
      }
    }
  }

  protected cancel(): void {
    const run = this.run();
    if (run) {
      void this.control.cancelJob(run.job);
    }
  }
}

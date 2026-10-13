import {
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  effect,
  inject,
  viewChild,
} from '@angular/core';
import { RouterLink } from '@angular/router';

import { IconComponent } from '../../components/icon/icon.component';
import { JobCardComponent } from '../../components/job-card/job-card.component';
import { ControlService } from '../../global/services/control/control.service';


@Component({
  selector: 'app-dashboard',
  imports: [RouterLink, IconComponent, JobCardComponent],
  templateUrl: './dashboard.page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class DashboardPage {
  protected readonly control = inject(ControlService);
  private readonly output = viewChild<ElementRef<HTMLElement>>('output');
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

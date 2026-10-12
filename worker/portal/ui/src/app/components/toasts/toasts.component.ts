import { ChangeDetectionStrategy, Component, inject } from '@angular/core';

import { ToastService } from '../../global/services/toast/toast.service';
import { IconComponent } from '../icon/icon.component';

@Component({
  selector: 'app-toasts',
  imports: [IconComponent],
  template: `
    <div class="toasts" aria-live="polite">
      @for (toast of toasts.toasts(); track toast.id) {
        <div class="toast" [class]="toast.kind" role="status">
          <app-icon [name]="toast.kind === 'success' ? 'check' : 'alert'" />
          <span>{{ toast.message }}</span>
          <button class="icon" type="button" aria-label="Dismiss" (click)="toasts.dismiss(toast.id)">
            <app-icon name="close" />
          </button>
        </div>
      }
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ToastsComponent {
  protected readonly toasts = inject(ToastService);
}

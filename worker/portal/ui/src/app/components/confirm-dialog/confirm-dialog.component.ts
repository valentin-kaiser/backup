import {
  ChangeDetectionStrategy,
  Component,
  ElementRef,
  effect,
  inject,
  viewChild,
} from '@angular/core';

import { ConfirmService } from '../../global/services/confirm/confirm.service';

/** Shows the questions of the ConfirmService, there is one in the app shell. */
@Component({
  selector: 'app-confirm-dialog',
  template: `
    <dialog #dialog (cancel)="confirm.answer(false)">
      @if (confirm.request(); as request) {
        <h3>{{ request.title }}</h3>
        <p>{{ request.message }}</p>
        <div class="actions">
          <button type="button" class="secondary" (click)="confirm.answer(false)">Cancel</button>
          <button type="button" [class]="request.danger ? 'danger' : 'primary'" (click)="confirm.answer(true)">
            {{ request.action }}
          </button>
        </div>
      }
    </dialog>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ConfirmDialogComponent {
  protected readonly confirm = inject(ConfirmService);
  private readonly dialog = viewChild.required<ElementRef<HTMLDialogElement>>('dialog');

  constructor() {
    effect(() => {
      const element = this.dialog().nativeElement;
      if (this.confirm.request()) {
        if (!element.open) {
          element.showModal();
        }
      } else if (element.open) {
        element.close();
      }
    });
  }
}

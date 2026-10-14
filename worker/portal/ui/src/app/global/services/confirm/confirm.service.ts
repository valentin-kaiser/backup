import { Injectable, signal } from '@angular/core';

export interface ConfirmRequest {
  title: string;
  message: string;
  /** Label of the button that confirms, e.g. "Delete". */
  action: string;
  /** Marks the action as destructive. */
  danger?: boolean;
}

/** Asks the user before something that cannot be undone, answered by the dialog in the app shell. */
@Injectable({ providedIn: 'root' })
export class ConfirmService {
  readonly request = signal<ConfirmRequest | null>(null);
  private resolve: ((confirmed: boolean) => void) | null = null;

  ask(request: ConfirmRequest): Promise<boolean> {
    this.resolve?.(false);
    this.request.set(request);
    return new Promise((resolve) => (this.resolve = resolve));
  }

  answer(confirmed: boolean): void {
    this.request.set(null);
    this.resolve?.(confirmed);
    this.resolve = null;
  }
}

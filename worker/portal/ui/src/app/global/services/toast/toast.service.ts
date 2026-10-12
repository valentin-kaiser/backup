import { Injectable, signal } from '@angular/core';

export interface Toast {
  id: number;
  kind: 'success' | 'error';
  message: string;
}

const LIFETIME_MS = 5000;

/** Short messages about the outcome of an action. */
@Injectable({ providedIn: 'root' })
export class ToastService {
  readonly toasts = signal<Toast[]>([]);
  private nextId = 1;

  success(message: string): void {
    this.add('success', message);
  }

  error(message: string): void {
    this.add('error', message);
  }

  dismiss(id: number): void {
    this.toasts.update((toasts) => toasts.filter((toast) => toast.id !== id));
  }

  private add(kind: Toast['kind'], message: string): void {
    const id = this.nextId++;
    this.toasts.update((toasts) => [...toasts, { id, kind, message }]);
    setTimeout(() => this.dismiss(id), LIFETIME_MS);
  }
}

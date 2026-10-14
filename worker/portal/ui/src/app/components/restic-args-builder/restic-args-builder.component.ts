import { ChangeDetectionStrategy, Component, computed, effect, input, model, signal, untracked } from '@angular/core';

import { ARG_GROUPS, ArgGroup, ArgSpec, ArgValue, ArgValues, JobType, buildArgs, parseArgs, quoteArg, splitArgs } from '../../global/utils/restic-args';
import { IconComponent } from '../icon/icon.component';

/**
 * A form for the restic options of a job. The value is the argument string the daemon stores, the form reads
 * it and writes it back, options it does not know are kept in "Other arguments". Nothing is written until
 * the user changes something, so existing arguments are never rewritten just by opening the form.
 */
@Component({
  selector: 'app-restic-args-builder',
  imports: [IconComponent],
  templateUrl: './restic-args-builder.component.html',
  styleUrl: './restic-args-builder.component.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ResticArgsBuilderComponent {
  /** The kind of job, it decides which options are offered. */
  readonly type = input.required<JobType>();
  /** The arguments as one string. */
  readonly args = model('');

  protected readonly values = signal<ArgValues>({});
  protected readonly extra = signal('');

  protected readonly groups = computed<ArgGroup[]>(() => ARG_GROUPS[this.type()] ?? []);
  /** The words of the command restic is started with, for a last look before saving. */
  protected readonly preview = computed(() => ['restic', this.type(), ...(splitArgs(this.args()) ?? [this.args()])].map(quoteArg).join(' '));
  protected readonly broken = computed(() => splitArgs(this.args()) === null);

  /** The string the form wrote last, anything else came from the outside. */
  private emitted: string | null = null;
  private parsedFor: JobType | null = null;

  constructor() {
    effect(() => {
      const args = this.args();
      const type = this.type();
      untracked(() => {
        // The options differ per type, so a new type reads the arguments again.
        if (args !== this.emitted || type !== this.parsedFor) {
          this.emitted = null;
          this.parsedFor = type;
          const parsed = parseArgs(type, args);
          this.values.set(parsed.values);
          this.extra.set(parsed.extra);
        }
      });
    });
  }

  protected flag(spec: ArgSpec): string {
    return `--${spec.flag}`;
  }

  protected bool(spec: ArgSpec): boolean {
    return this.values()[spec.flag] === true;
  }

  protected text(spec: ArgSpec): string {
    const value = this.values()[spec.flag];
    return typeof value === 'string' ? value : '';
  }

  protected list(spec: ArgSpec): string[] {
    const value = this.values()[spec.flag];
    return Array.isArray(value) ? value : [];
  }

  protected set(spec: ArgSpec, value: ArgValue): void {
    this.values.update((values) => ({ ...values, [spec.flag]: value }));
    this.write();
  }

  protected add(spec: ArgSpec, input: HTMLInputElement): void {
    const item = input.value.trim();
    if (item && !this.list(spec).includes(item)) {
      this.set(spec, [...this.list(spec), item]);
    }
    input.value = '';
  }

  protected remove(spec: ArgSpec, item: string): void {
    this.set(spec, this.list(spec).filter((entry) => entry !== item));
  }

  protected setExtra(value: string): void {
    this.extra.set(value);
    this.write();
  }

  private write(): void {
    const args = buildArgs(this.type(), this.values(), this.extra());
    this.emitted = args;
    this.args.set(args);
  }
}

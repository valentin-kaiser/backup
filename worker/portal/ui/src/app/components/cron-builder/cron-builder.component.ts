import { ChangeDetectionStrategy, Component, computed, effect, input, model, signal, untracked } from '@angular/core';

import {
  CRON_FIELD_LABELS,
  CRON_FIELD_RANGES,
  CRON_FIELD_TYPES,
  CRON_PRESETS,
  CronFieldType,
  DAY_NAMES,
  MONTH_NAMES,
  describeCron,
  expandCronField,
  nextRuns,
  splitCronExpression,
  validateCron,
} from '../../global/utils/cron';
import { IconComponent } from '../icon/icon.component';

type FieldMode = 'every' | 'specific' | 'range' | 'interval' | 'list';

/** What the controls of one field show. The expression is built from it. */
interface FieldConfig {
  mode: FieldMode;
  value?: number;
  start?: number;
  end?: number;
  base?: number;
  step?: number;
  list?: number[];
}

type Configs = Record<CronFieldType, FieldConfig>;
type BuilderMode = 'presets' | 'custom';

const MODES: { value: FieldMode; label: string }[] = [
  { value: 'every', label: 'Every' },
  { value: 'specific', label: 'At' },
  { value: 'range', label: 'Range' },
  { value: 'interval', label: 'Interval' },
  { value: 'list', label: 'List' },
];

/** What Every means in each field, in the singular. */
const SINGULAR: Record<CronFieldType, string> = {
  minute: 'minute',
  hour: 'hour',
  dayOfMonth: 'day of the month',
  month: 'month',
  dayOfWeek: 'day of the week',
};

const UNITS: Record<CronFieldType, string> = {
  minute: 'minutes',
  hour: 'hours',
  dayOfMonth: 'days',
  month: 'months',
  dayOfWeek: 'days',
};

const NEXT_RUNS = 5;

/**
 * A cron expression with a builder: type it, pick a preset, or compose it field by field. The expression
 * is the value, the builder only edits it, so there is no apply step. After the field it lists the next runs.
 */
@Component({
  selector: 'app-cron-builder',
  imports: [IconComponent],
  templateUrl: './cron-builder.component.html',
  styleUrl: './cron-builder.component.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CronBuilderComponent {
  /** The cron expression. */
  readonly value = model('');
  /** Whether the builder is shown. */
  readonly open = signal(false);
  /** An error from the outside, e.g. a rule of the form. */
  readonly error = input('');

  protected readonly presets = CRON_PRESETS;
  protected readonly types = CRON_FIELD_TYPES;
  protected readonly modes = MODES;
  protected readonly builderMode = signal<BuilderMode>('presets');
  protected readonly configs = signal<Configs>(defaultConfigs());

  /** An empty field is only a problem when the form says a schedule is required. */
  protected readonly problem = computed(() => this.error() || (this.value().trim() ? validateCron(this.value()) : ''));
  protected readonly description = computed(() => describeCron(this.value()));
  protected readonly upcoming = computed(() => nextRuns(this.value(), NEXT_RUNS));
  protected readonly activePreset = computed(() => this.presets.find((p) => p.expression === this.value().trim())?.expression ?? '');
  protected readonly fields = computed(() => splitCronExpression(this.value()) ?? []);

  /** The last expression the builder wrote itself, anything else came from the outside. */
  private emitted: string | null = null;

  constructor() {
    // Typing in the field or a new value from the form reloads the controls, the builder's own edits do not.
    effect(() => {
      const value = this.value();
      untracked(() => {
        if (value !== this.emitted) {
          this.emitted = null;
          this.load(value);
        }
      });
    });
  }

  protected toggle(): void {
    this.open.update((open) => !open);
    if (this.open()) {
      this.builderMode.set(this.activePreset() ? 'presets' : 'custom');
    }
  }

  protected selectPreset(expression: string): void {
    this.write(expression);
    this.load(expression);
  }

  protected label(type: CronFieldType): string {
    return CRON_FIELD_LABELS[type];
  }

  protected range(type: CronFieldType): { min: number; max: number } {
    return CRON_FIELD_RANGES[type];
  }

  protected singular(type: CronFieldType): string {
    return SINGULAR[type];
  }

  protected unit(type: CronFieldType): string {
    return UNITS[type];
  }

  /** Small sets are picked by name or from a list, big ones typed. */
  protected named(type: CronFieldType): boolean {
    return type === 'hour' || type === 'month' || type === 'dayOfWeek';
  }

  /** Every value of a small set as a choice, with a readable name. */
  protected options(type: CronFieldType): { value: number; label: string }[] {
    const { min, max } = CRON_FIELD_RANGES[type];
    return Array.from({ length: max - min + 1 }, (_, i) => ({ value: min + i, label: this.valueLabel(type, min + i) }));
  }

  protected valueLabel(type: CronFieldType, value: number): string {
    if (type === 'dayOfWeek') {
      return DAY_NAMES[value].charAt(0) + DAY_NAMES[value].slice(1).toLowerCase();
    }
    if (type === 'month') {
      return MONTH_NAMES[value - 1].charAt(0) + MONTH_NAMES[value - 1].slice(1).toLowerCase();
    }
    return type === 'hour' ? `${String(value).padStart(2, '0')}:00` : String(value);
  }

  protected config(type: CronFieldType): FieldConfig {
    return this.configs()[type];
  }

  protected setMode(type: CronFieldType, mode: FieldMode): void {
    const { min, max } = CRON_FIELD_RANGES[type];
    const fresh: Record<FieldMode, FieldConfig> = {
      every: { mode },
      specific: { mode, value: this.config(type).value ?? min },
      range: { mode, start: min, end: max },
      interval: { mode, step: 1 },
      list: { mode, list: [min] },
    };
    this.patch(type, fresh[mode], true);
  }

  protected setNumber(type: CronFieldType, key: 'value' | 'start' | 'end' | 'base' | 'step', raw: string): void {
    const parsed = raw.trim() === '' ? undefined : parseInt(raw, 10);
    this.patch(type, { [key]: Number.isNaN(parsed) ? undefined : parsed });
  }

  protected toggleListValue(type: CronFieldType, value: number): void {
    const list = this.config(type).list ?? [];
    const next = list.includes(value) ? list.filter((v) => v !== value) : [...list, value].sort((a, b) => a - b);
    this.patch(type, { list: next });
  }

  protected addToList(type: CronFieldType, input: HTMLInputElement): void {
    const value = parseInt(input.value, 10);
    const { min, max } = CRON_FIELD_RANGES[type];
    if (Number.isNaN(value) || value < min || value > max) {
      return;
    }
    const list = [...new Set([...(this.config(type).list ?? []), value])].sort((a, b) => a - b);
    this.patch(type, { list });
    input.value = '';
  }

  protected removeFromList(type: CronFieldType, value: number): void {
    this.patch(type, { list: (this.config(type).list ?? []).filter((v) => v !== value) });
  }

  protected format(date: Date): string {
    return date.toLocaleString(undefined, { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' });
  }

  private write(expression: string): void {
    this.emitted = expression;
    this.value.set(expression);
  }

  /** Reads an expression into the controls. Shapes the controls cannot show become a list. */
  private load(expression: string): void {
    const fields = splitCronExpression(expression);
    if (!fields) {
      return;
    }
    const configs = defaultConfigs();
    CRON_FIELD_TYPES.forEach((type, index) => (configs[type] = parseField(fields[index], type)));
    this.configs.set(configs);
  }

  private patch(type: CronFieldType, change: Partial<FieldConfig>, replace = false): void {
    const configs = { ...this.configs() };
    configs[type] = replace ? (change as FieldConfig) : { ...configs[type], ...change };
    this.configs.set(configs);
    this.write(CRON_FIELD_TYPES.map((field) => fieldExpression(field, configs[field])).join(' '));
  }
}

function defaultConfigs(): Configs {
  return {
    minute: { mode: 'every' },
    hour: { mode: 'every' },
    dayOfMonth: { mode: 'every' },
    month: { mode: 'every' },
    dayOfWeek: { mode: 'every' },
  };
}

function parseField(field: string, type: CronFieldType): FieldConfig {
  const values = expandCronField(field, type);
  if (!values || field === '*' || field === '?') {
    return { mode: 'every' };
  }
  const { min, max } = CRON_FIELD_RANGES[type];
  const step = /^(\*|\d+)\/(\d+)$/.exec(field);
  if (step) {
    return { mode: 'interval', base: step[1] === '*' ? undefined : Number(step[1]), step: Number(step[2]) };
  }
  if (values.length === 1) {
    return { mode: 'specific', value: values[0] };
  }
  const contiguous = values.every((v, i) => i === 0 || v === values[i - 1] + 1);
  if (contiguous && values.length < max - min + 1 && !field.includes(',')) {
    return { mode: 'range', start: values[0], end: values[values.length - 1] };
  }
  return { mode: 'list', list: values };
}

function fieldExpression(type: CronFieldType, config: FieldConfig): string {
  const { min, max } = CRON_FIELD_RANGES[type];
  switch (config.mode) {
    case 'specific':
      return `${config.value ?? min}`;
    case 'range':
      return `${config.start ?? min}-${config.end ?? max}`;
    case 'interval':
      return `${config.base ?? '*'}/${config.step ?? 1}`;
    case 'list':
      return (config.list?.length ? config.list : [min]).join(',');
    default:
      return '*';
  }
}

/**
 * Five-field cron as the daemon's scheduler (go-core queue) parses it: minute hour day-of-month month
 * day-of-week. A field is `*`, a value, a range `a-b`, a list `a,b,c` or a step `*\/n`, `a/n`, `a-b/n`.
 * Ranges and lists cannot be mixed within one field. Month and weekday names (JAN, MON) are accepted.
 * Unlike classic cron, a run needs the day of the month AND the weekday to match.
 */
export type CronFieldType = 'minute' | 'hour' | 'dayOfMonth' | 'month' | 'dayOfWeek';

export const CRON_FIELD_TYPES: CronFieldType[] = ['minute', 'hour', 'dayOfMonth', 'month', 'dayOfWeek'];

export const CRON_FIELD_RANGES: Record<CronFieldType, { min: number; max: number }> = {
  minute: { min: 0, max: 59 },
  hour: { min: 0, max: 23 },
  dayOfMonth: { min: 1, max: 31 },
  month: { min: 1, max: 12 },
  dayOfWeek: { min: 0, max: 6 },
};

export const CRON_FIELD_LABELS: Record<CronFieldType, string> = {
  minute: 'Minute',
  hour: 'Hour',
  dayOfMonth: 'Day of month',
  month: 'Month',
  dayOfWeek: 'Day of week',
};

export const MONTH_NAMES = ['JAN', 'FEB', 'MAR', 'APR', 'MAY', 'JUN', 'JUL', 'AUG', 'SEP', 'OCT', 'NOV', 'DEC'];
export const DAY_NAMES = ['SUN', 'MON', 'TUE', 'WED', 'THU', 'FRI', 'SAT'];

const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
const MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'];

const FIELD_NAMES: Partial<Record<CronFieldType, string[]>> = { month: MONTH_NAMES, dayOfWeek: DAY_NAMES };
const NAME_OFFSET: Partial<Record<CronFieldType, number>> = { month: 1, dayOfWeek: 0 };

/** A schedule the job form offers as a shortcut. */
export interface CronPreset {
  label: string;
  description: string;
  expression: string;
}

export const CRON_PRESETS: CronPreset[] = [
  { label: 'Every hour', description: 'At minute 0 of every hour.', expression: '0 * * * *' },
  { label: 'Every 15 minutes', description: 'Four times an hour, around the clock.', expression: '*/15 * * * *' },
  { label: 'Every night', description: 'Every day at 02:00.', expression: '0 2 * * *' },
  { label: 'Every day at midnight', description: 'Every day at 00:00.', expression: '0 0 * * *' },
  { label: 'Weekdays in the evening', description: 'Monday to Friday at 22:00.', expression: '0 22 * * 1-5' },
  { label: 'Every Sunday', description: 'Sundays at 03:00.', expression: '0 3 * * 0' },
  { label: 'Every Wednesday', description: 'Wednesdays at 05:00.', expression: '0 5 * * 3' },
  { label: 'Every month', description: 'The first day of the month at 04:00.', expression: '0 4 1 * *' },
];

/** Replaces month and weekday names with their numbers. */
function normalizeNames(field: string, type: CronFieldType): string {
  const names = FIELD_NAMES[type];
  if (!names) {
    return field;
  }
  return names.reduce((result, name, index) => result.split(name).join(String(index + (NAME_OFFSET[type] ?? 0))), field);
}

function parseValue(raw: string, min: number, max: number): number | null {
  if (!/^\d+$/.test(raw)) {
    return null;
  }
  const value = Number(raw);
  return value >= min && value <= max ? value : null;
}

function span(start: number, end: number, step = 1): number[] {
  const values: number[] = [];
  for (let value = start; value <= end; value += step) {
    values.push(value);
  }
  return values;
}

/** Expands one cron field into its sorted values, or null when it is invalid. */
export function expandCronField(field: string, type: CronFieldType): number[] | null {
  const { min, max } = CRON_FIELD_RANGES[type];
  let normalized = field.toUpperCase();
  if (normalized === '?' && type !== 'minute' && type !== 'hour') {
    normalized = '*';
  }
  normalized = normalizeNames(normalized, type);

  if (normalized === '*') {
    return span(min, max);
  }
  if (normalized.includes('/')) {
    const parts = normalized.split('/');
    const step = parts.length === 2 ? parseValue(parts[1], 1, Number.MAX_SAFE_INTEGER) : null;
    if (step === null) {
      return null;
    }
    if (parts[0] === '*') {
      return span(min, max, step);
    }
    if (parts[0].includes('-')) {
      const range = parts[0].split('-');
      const start = range.length === 2 ? parseValue(range[0], min, max) : null;
      const end = range.length === 2 ? parseValue(range[1], min, max) : null;
      return start === null || end === null || end < start ? null : span(start, end, step);
    }
    const start = parseValue(parts[0], min, max);
    return start === null ? null : span(start, max, step);
  }
  if (normalized.includes('-')) {
    const range = normalized.split('-');
    const start = range.length === 2 ? parseValue(range[0], min, max) : null;
    const end = range.length === 2 ? parseValue(range[1], min, max) : null;
    return start === null || end === null || end < start ? null : span(start, end);
  }
  if (normalized.includes(',')) {
    const values = normalized.split(',').map((part) => parseValue(part.trim(), min, max));
    if (values.some((value) => value === null)) {
      return null;
    }
    return [...new Set(values as number[])].sort((a, b) => a - b);
  }
  const value = parseValue(normalized, min, max);
  return value === null ? null : [value];
}

/** Splits an expression into its five fields, or null when the count is wrong. */
export function splitCronExpression(expression: string): string[] | null {
  const fields = expression.trim().split(/\s+/).filter(Boolean);
  return fields.length === CRON_FIELD_TYPES.length ? fields : null;
}

/** Returns why the expression is invalid, or null when it is fine. */
export function validateCron(expression: string): string | null {
  if (!expression.trim()) {
    return 'A schedule is required';
  }
  const fields = splitCronExpression(expression);
  if (!fields) {
    return 'Use five fields: minute hour day-of-month month day-of-week';
  }
  for (const [index, type] of CRON_FIELD_TYPES.entries()) {
    if (!expandCronField(fields[index], type)) {
      const { min, max } = CRON_FIELD_RANGES[type];
      return `Invalid ${CRON_FIELD_LABELS[type].toLowerCase()} field "${fields[index]}" (allowed ${min}-${max})`;
    }
  }
  return null;
}

const pad = (n: number | string) => String(n).padStart(2, '0');

function valueName(value: number, type: CronFieldType): string {
  if (type === 'dayOfWeek') {
    return WEEKDAYS[value];
  }
  if (type === 'month') {
    return MONTHS[value - 1];
  }
  return String(value);
}

/** Sorted values as runs, e.g. "Monday–Friday, Sunday". */
function formatValues(values: number[], type: CronFieldType): string {
  const runs: string[] = [];
  let start = 0;
  for (let index = 1; index <= values.length; index++) {
    if (index < values.length && values[index] === values[index - 1] + 1) {
      continue;
    }
    const first = values[start];
    const last = values[index - 1];
    runs.push(last - first >= 2 ? `${valueName(first, type)}–${valueName(last, type)}` : values.slice(start, index).map((v) => valueName(v, type)).join(', '));
    start = index;
  }
  return runs.join(', ');
}

/** Describes an expression in words. Returns an empty string for an invalid one. */
export function describeCron(expression: string): string {
  if (validateCron(expression)) {
    return '';
  }
  const fields = splitCronExpression(expression) as string[];
  const [minute, hour, day, month, weekday] = fields;
  const isNumber = (s: string) => /^\d+$/.test(s);

  const every = /^\*\/(\d+)$/.exec(minute);
  if (every && hour === '*' && day === '*' && month === '*' && weekday === '*') {
    return `Every ${every[1]} minutes`;
  }
  if (isNumber(minute) && hour === '*' && day === '*' && month === '*' && weekday === '*') {
    return minute === '0' ? 'Every hour' : `Every hour at minute ${minute}`;
  }
  if (isNumber(minute) && isNumber(hour) && month === '*') {
    const time = `${pad(hour)}:${pad(minute)}`;
    if (day === '*' && weekday === '*') {
      return `Every day at ${time}`;
    }
    if (day === '*' && isNumber(weekday)) {
      return `Every ${WEEKDAYS[Number(weekday)]} at ${time}`;
    }
    if (isNumber(day) && weekday === '*') {
      return `On day ${day} of every month at ${time}`;
    }
  }

  // Everything else piece by piece.
  const parts: string[] = [];
  for (const [index, type] of CRON_FIELD_TYPES.entries()) {
    const values = expandCronField(fields[index], type) ?? [];
    const { min, max } = CRON_FIELD_RANGES[type];
    if (values.length === max - min + 1) {
      continue;
    }
    const text = type === 'dayOfWeek' || type === 'month' ? formatValues(values, type) : values.length > 8 ? `${values.slice(0, 8).join(', ')}, …` : values.join(', ');
    switch (type) {
      case 'minute':
        parts.push(`at minute ${text}`);
        break;
      case 'hour':
        parts.push(`past hour ${text}`);
        break;
      case 'dayOfMonth':
        parts.push(`on day ${text} of the month`);
        break;
      case 'month':
        parts.push(`in ${text}`);
        break;
      case 'dayOfWeek':
        parts.push(`on ${text}`);
        break;
    }
  }
  return parts.length ? parts.join(', ').replace(/^./, (c) => c.toUpperCase()) : 'Every minute';
}

/**
 * The next times the expression fires after `from`, in the time zone of the browser. The daemon uses its own
 * time zone, so the clock times can differ when the two are not in the same zone.
 */
export function nextRuns(expression: string, count: number, from: Date = new Date()): Date[] {
  const fields = validateCron(expression) ? null : splitCronExpression(expression);
  if (!fields) {
    return [];
  }
  const [minutes, hours, days, months, weekdays] = CRON_FIELD_TYPES.map(
    (type, index) => new Set(expandCronField(fields[index], type) ?? []),
  );
  const sortedHours = [...hours].sort((a, b) => a - b);
  const sortedMinutes = [...minutes].sort((a, b) => a - b);

  const runs: Date[] = [];
  const day = new Date(from.getFullYear(), from.getMonth(), from.getDate());
  // Four years cover the rarest schedules, such as February 29th.
  for (let guard = 0; guard < 366 * 4 && runs.length < count; guard++, day.setDate(day.getDate() + 1)) {
    if (!months.has(day.getMonth() + 1) || !days.has(day.getDate()) || !weekdays.has(day.getDay())) {
      continue;
    }
    for (const hour of sortedHours) {
      for (const minute of sortedMinutes) {
        const run = new Date(day.getFullYear(), day.getMonth(), day.getDate(), hour, minute);
        if (run.getTime() > from.getTime() && runs.length < count) {
          runs.push(run);
        }
      }
    }
  }
  return runs;
}

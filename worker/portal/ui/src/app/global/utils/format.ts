import { Run, RunStatus } from '../model/backup/api/v1/control';

/** Lower case name of a status as used in the CSS classes, e.g. "success". */
export function statusName(status: RunStatus): string {
  switch (status) {
    case RunStatus.RUN_STATUS_QUEUED:
      return 'queued';
    case RunStatus.RUN_STATUS_RUNNING:
      return 'running';
    case RunStatus.RUN_STATUS_SUCCESS:
      return 'success';
    case RunStatus.RUN_STATUS_FAILED:
      return 'failed';
    case RunStatus.RUN_STATUS_CANCELLED:
      return 'cancelled';
    case RunStatus.RUN_STATUS_SKIPPED:
      return 'skipped';
    default:
      return 'unknown';
  }
}

export function isActive(status: RunStatus): boolean {
  return status === RunStatus.RUN_STATUS_QUEUED || status === RunStatus.RUN_STATUS_RUNNING;
}

export function formatTime(time: Date | undefined): string {
  return time ? time.toLocaleString() : '–';
}

/** "3 minutes ago" or "in 2 hours", the exact time is better placed in a tooltip. */
export function formatRelative(time: Date | undefined, now: Date = new Date()): string {
  if (!time) {
    return '–';
  }
  const seconds = Math.round((time.getTime() - now.getTime()) / 1000);
  const abs = Math.abs(seconds);
  const units: [number, Intl.RelativeTimeFormatUnit][] = [
    [60, 'second'],
    [3600, 'minute'],
    [86400, 'hour'],
    [86400 * 30, 'day'],
  ];
  const format = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });
  let divisor = 1;
  for (const [limit, unit] of units) {
    if (abs < limit) {
      return format.format(Math.round(seconds / divisor), unit);
    }
    divisor = limit;
  }
  return format.format(Math.round(seconds / (86400 * 30)), 'month');
}

/** Duration of a run, counted up to now while it is still running. */
export function formatDuration(run: Run, now: Date = new Date()): string {
  if (!run.startTime) {
    return '–';
  }
  const end = run.finishTime ?? now;
  const seconds = Math.max(0, Math.round((end.getTime() - run.startTime.getTime()) / 1000));
  if (seconds >= 3600) {
    return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`;
  }
  if (seconds >= 60) {
    return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
  }
  return `${seconds}s`;
}

export function formatBytes(bytes: number): string {
  if (!bytes) {
    return '0 B';
  }
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  const exponent = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)));
  const value = bytes / 1024 ** exponent;
  return `${value >= 100 || exponent === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[exponent]}`;
}

export const JOB_TYPES = [
  { value: 'backup', label: 'Backup', hint: 'Copies a directory into a restic repository.' },
  { value: 'forget', label: 'Forget & prune', hint: 'Removes old snapshots by a retention policy and frees the space.' },
  { value: 'check', label: 'Check', hint: 'Verifies the integrity of a repository.' },
] as const;

export function jobTypeLabel(type: string): string {
  return JOB_TYPES.find((t) => t.value === type)?.label ?? type;
}

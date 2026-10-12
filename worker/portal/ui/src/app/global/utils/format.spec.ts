import { Run, RunStatus } from '../model/backup/api/v1/control';
import {
  formatBytes,
  formatDuration,
  formatRelative,
  formatTime,
  isActive,
  jobTypeLabel,
  statusName,
} from './format';

const run = (start?: string, finish?: string): Run => ({
  id: 'a',
  job: 'backup',
  trigger: 'manual',
  status: RunStatus.RUN_STATUS_SUCCESS,
  queueTime: undefined,
  startTime: start ? new Date(start) : undefined,
  finishTime: finish ? new Date(finish) : undefined,
  exitCode: 0,
  error: '',
  output: [],
});

describe('format', () => {
  it('names statuses like the css classes', () => {
    expect(statusName(RunStatus.RUN_STATUS_SUCCESS)).toBe('success');
    expect(statusName(RunStatus.RUN_STATUS_UNSPECIFIED)).toBe('unknown');
  });

  it('knows which statuses are still active', () => {
    expect(isActive(RunStatus.RUN_STATUS_QUEUED)).toBe(true);
    expect(isActive(RunStatus.RUN_STATUS_RUNNING)).toBe(true);
    expect(isActive(RunStatus.RUN_STATUS_FAILED)).toBe(false);
  });

  it('formats durations', () => {
    expect(formatDuration(run())).toBe('–');
    expect(formatDuration(run('2024-01-01T00:00:00Z', '2024-01-01T00:00:42Z'))).toBe('42s');
    expect(formatDuration(run('2024-01-01T00:00:00Z', '2024-01-01T00:03:05Z'))).toBe('3m 5s');
    expect(formatDuration(run('2024-01-01T00:00:00Z', '2024-01-01T02:10:00Z'))).toBe('2h 10m');
  });

  it('counts running runs up to now', () => {
    expect(formatDuration(run('2024-01-01T00:00:00Z'), new Date('2024-01-01T00:00:10Z'))).toBe('10s');
  });

  it('formats missing times', () => {
    expect(formatTime(undefined)).toBe('–');
    expect(formatRelative(undefined)).toBe('–');
  });

  it('formats times relative to now, past and future', () => {
    const now = new Date('2024-01-01T12:00:00Z');
    expect(formatRelative(new Date('2024-01-01T11:55:00Z'), now)).toBe('5 minutes ago');
    expect(formatRelative(new Date('2024-01-01T14:00:00Z'), now)).toBe('in 2 hours');
    expect(formatRelative(new Date('2023-12-30T12:00:00Z'), now)).toBe('2 days ago');
  });

  it('formats sizes', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(1536)).toBe('1.5 KiB');
    expect(formatBytes(5 * 1024 ** 3)).toBe('5.0 GiB');
    expect(formatBytes(250 * 1024 ** 2)).toBe('250 MiB');
  });

  it('labels job types', () => {
    expect(jobTypeLabel('forget')).toBe('Forget & prune');
    expect(jobTypeLabel('other')).toBe('other');
  });
});

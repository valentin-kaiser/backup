import { CRON_PRESETS, describeCron, expandCronField, nextRuns, validateCron } from './cron';

describe('cron', () => {
  it('accepts five fields and rejects everything else', () => {
    expect(validateCron('0 0 * * *')).toBeNull();
    expect(validateCron('*/15 1-5 1,15 * mon')).toBeNull();
    expect(validateCron('0 0 1 JAN-MAR *')).toBeNull();
    expect(validateCron('')).toBeTruthy();
    expect(validateCron('every day')).toBeTruthy();
    expect(validateCron('0 0 * *')).toBeTruthy();
    expect(validateCron('0 0 * * * *')).toBeTruthy();
    expect(validateCron('0 0 * * $(id)')).toBeTruthy();
  });

  it('names the field that is wrong and what is allowed', () => {
    expect(validateCron('61 0 * * *')).toBe('Invalid minute field "61" (allowed 0-59)');
    expect(validateCron('0 24 * * *')).toContain('hour');
    expect(validateCron('0 0 32 * *')).toContain('day of month');
    expect(validateCron('0 0 * 13 *')).toContain('month');
    expect(validateCron('0 0 * * 7')).toContain('day of week');
    expect(validateCron('0 0 * * 5-1')).toContain('day of week');
  });

  it('expands fields like the daemon does', () => {
    expect(expandCronField('*/20', 'minute')).toEqual([0, 20, 40]);
    expect(expandCronField('10-30/10', 'minute')).toEqual([10, 20, 30]);
    expect(expandCronField('5/20', 'minute')).toEqual([5, 25, 45]);
    expect(expandCronField('1,3,5', 'hour')).toEqual([1, 3, 5]);
    expect(expandCronField('MON-FRI', 'dayOfWeek')).toEqual([1, 2, 3, 4, 5]);
    expect(expandCronField('DEC', 'month')).toEqual([12]);
    expect(expandCronField('?', 'dayOfMonth')).toHaveLength(31);
    expect(expandCronField('1-5,7', 'dayOfWeek')).toBeNull();
  });

  it('describes the common expressions in words', () => {
    expect(describeCron('0 0 * * *')).toBe('Every day at 00:00');
    expect(describeCron('30 1 * * *')).toBe('Every day at 01:30');
    expect(describeCron('0 3 * * 0')).toBe('Every Sunday at 03:00');
    expect(describeCron('0 5 * * 3')).toBe('Every Wednesday at 05:00');
    expect(describeCron('0 * * * *')).toBe('Every hour');
    expect(describeCron('15 * * * *')).toBe('Every hour at minute 15');
    expect(describeCron('*/10 * * * *')).toBe('Every 10 minutes');
    expect(describeCron('0 4 1 * *')).toBe('On day 1 of every month at 04:00');
    expect(describeCron('nonsense')).toBe('');
  });

  it('describes the rest piece by piece', () => {
    expect(describeCron('0 22 * * 1-5')).toBe('At minute 0, past hour 22, on Monday–Friday');
    expect(describeCron('0,30 8-10 * 6 *')).toBe('At minute 0, 30, past hour 8, 9, 10, in June');
    expect(describeCron('* * * * *')).toBe('Every minute');
  });

  it('offers presets that are valid and described', () => {
    for (const preset of CRON_PRESETS) {
      expect(validateCron(preset.expression)).toBeNull();
      expect(describeCron(preset.expression)).not.toBe('');
    }
  });

  describe('nextRuns', () => {
    const from = new Date(2024, 0, 10, 12, 30); // Wednesday, 10 January 2024, 12:30

    it('lists the coming runs in order, never including the current minute', () => {
      const runs = nextRuns('30 12 * * *', 3, from);
      expect(runs.map((r) => [r.getMonth(), r.getDate(), r.getHours(), r.getMinutes()])).toEqual([
        [0, 11, 12, 30],
        [0, 12, 12, 30],
        [0, 13, 12, 30],
      ]);
    });

    it('runs several times a day', () => {
      const runs = nextRuns('0 6,18 * * *', 3, from);
      expect(runs.map((r) => `${r.getDate()} ${r.getHours()}`)).toEqual(['10 18', '11 6', '11 18']);
    });

    it('honours weekdays and month names', () => {
      const runs = nextRuns('0 3 * * SUN', 2, from);
      expect(runs.map((r) => [r.getDate(), r.getDay()])).toEqual([
        [14, 0],
        [21, 0],
      ]);
      expect(nextRuns('0 0 1 FEB *', 1, from)[0].getMonth()).toBe(1);
    });

    it('needs the day of the month and the weekday to match, like the daemon', () => {
      // The 13th that is a Friday, not every 13th and every Friday.
      const runs = nextRuns('0 0 13 * FRI', 2, from);
      for (const run of runs) {
        expect(run.getDate()).toBe(13);
        expect(run.getDay()).toBe(5);
      }
      expect(runs).toHaveLength(2);
    });

    it('finds rare dates and gives up on impossible ones', () => {
      expect(nextRuns('0 0 29 2 *', 1, from)[0].getFullYear()).toBe(2024);
      expect(nextRuns('0 0 31 2 *', 1, from)).toEqual([]);
    });

    it('returns nothing for an invalid expression', () => {
      expect(nextRuns('nonsense', 3, from)).toEqual([]);
    });
  });
});

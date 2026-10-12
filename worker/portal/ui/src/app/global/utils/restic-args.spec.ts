import { ARG_GROUPS, JobType, buildArgs, parseArgs, quoteArg, specsOf, splitArgs } from './restic-args';

describe('splitArgs', () => {
  // The same table as runner.TestSplitArgs in the daemon, the form must split exactly like it does.
  const cases: [string, string[] | null][] = [
    ['', []],
    ['--a  --b', ['--a', '--b']],
    ['--exclude "a b" \'c d\'', ['--exclude', 'a b', 'c d']],
    ['a\\ b', ['a b']],
    ['--x=$(id) ;ls', ['--x=$(id)', ';ls']],
    ['"open', null],
    ['trail\\', null],
    ['""', ['']],
  ];
  for (const [input, expected] of cases) {
    it(`splits ${JSON.stringify(input)}`, () => {
      expect(splitArgs(input)).toEqual(expected);
    });
  }
});

describe('quoteArg', () => {
  it('leaves plain words alone and quotes the rest', () => {
    expect(quoteArg('--exclude-caches')).toBe('--exclude-caches');
    expect(quoteArg('*.tmp')).toBe('"*.tmp"');
    expect(quoteArg('5%')).toBe('5%');
    expect(quoteArg('a b')).toBe('"a b"');
    expect(quoteArg('/a/b-c_d.txt')).toBe('/a/b-c_d.txt');
    expect(quoteArg('')).toBe('""');
  });

  it('gives every argument back unchanged through splitArgs', () => {
    for (const arg of ['a b', 'say "hi"', 'back\\slash', "it's", '$(id)', ';rm -rf', '*.tmp', '', 'two  spaces', 'tab\there']) {
      expect(splitArgs(quoteArg(arg))).toEqual([arg]);
    }
  });
});

describe('parseArgs', () => {
  it('reads the defaults of a backup job', () => {
    const { values, extra } = parseArgs('backup', '--exclude-caches --exclude-if-present=.nobackup --exclude-file=/.backupignore');
    expect(values).toEqual({ 'exclude-caches': true, 'exclude-if-present': '.nobackup', 'exclude-file': ['/.backupignore'] });
    expect(extra).toBe('');
  });

  it('accepts both forms of a value and short flags', () => {
    expect(parseArgs('backup', '--host nas --tag=a --tag b -x').values).toEqual({ host: 'nas', tag: ['a', 'b'], 'one-file-system': true });
  });

  it('reads the default retention of a forget job', () => {
    const { values, extra } = parseArgs('forget', '--prune --keep-last 7 --keep-daily 14 --keep-weekly 4 --keep-monthly 6 --keep-yearly 1');
    expect(values).toEqual({ prune: true, 'keep-last': '7', 'keep-daily': '14', 'keep-weekly': '4', 'keep-monthly': '6', 'keep-yearly': '1' });
    expect(extra).toBe('');
  });

  it('keeps what the form does not know, together with its value', () => {
    const { values, extra } = parseArgs('backup', '--exclude-caches --limit-upload 500 --verbose --exclude "a b"');
    expect(values).toEqual({ 'exclude-caches': true, exclude: ['a b'] });
    expect(extra).toBe('--limit-upload 500 --verbose');
  });

  it('only knows the options of its own type', () => {
    // --prune belongs to forget, in a check job it is not the form's business.
    expect(parseArgs('check', '--prune --read-data-subset=5%')).toEqual({ values: { 'read-data-subset': '5%' }, extra: '--prune' });
  });

  it('treats an explicit false as off', () => {
    expect(parseArgs('forget', '--prune=false').values).toEqual({ prune: false });
  });

  it('keeps a missing value instead of inventing one', () => {
    expect(parseArgs('backup', '--exclude-caches --host')).toEqual({ values: { 'exclude-caches': true }, extra: '--host' });
  });

  it('leaves text with broken quotes alone', () => {
    expect(parseArgs('backup', '--exclude "open')).toEqual({ values: {}, extra: '--exclude "open' });
  });
});

describe('buildArgs', () => {
  it('writes the options in the order of the form', () => {
    expect(buildArgs('backup', { host: 'nas', 'exclude-caches': true, exclude: ['*.tmp', 'node_modules'] }, '')).toBe(
      '--exclude "*.tmp" --exclude node_modules --exclude-caches --host nas',
    );
  });

  it('skips empty and switched off options', () => {
    expect(buildArgs('forget', { prune: false, 'keep-last': '', 'keep-daily': ' 14 ', tag: ['', ' x '], 'group-by': '' }, '')).toBe(
      '--keep-daily 14 --tag x',
    );
  });

  it('appends the unknown rest', () => {
    expect(buildArgs('backup', { 'exclude-caches': true }, '--limit-upload 500')).toBe('--exclude-caches --limit-upload 500');
    expect(buildArgs('check', {}, '  --verbose  ')).toBe('--verbose');
  });

  it('quotes values that need it', () => {
    expect(buildArgs('backup', { host: 'my host' }, '')).toBe('--host "my host"');
  });
});

describe('the specs', () => {
  const types: JobType[] = ['backup', 'forget', 'check'];

  it('have unique flags per type', () => {
    for (const type of types) {
      const flags = specsOf(type).map((spec) => spec.flag);
      expect(new Set(flags).size).toBe(flags.length);
    }
  });

  it('describe every field', () => {
    for (const type of types) {
      for (const group of ARG_GROUPS[type]) {
        for (const spec of group.specs) {
          expect(spec.label).toBeTruthy();
          if (spec.kind === 'select') {
            expect(spec.options?.length).toBeGreaterThan(1);
            expect(spec.options?.[0].value).toBe('');
          }
        }
      }
    }
  });

  it('survive a round trip of every option', () => {
    for (const type of types) {
      const values: Record<string, boolean | string | string[]> = {};
      for (const spec of specsOf(type)) {
        values[spec.flag] = spec.kind === 'bool' ? true : spec.kind === 'list' ? ['a b', 'c'] : spec.kind === 'select' ? (spec.options?.[1].value ?? '') : '7';
      }
      const args = buildArgs(type, values, '--unknown 1');
      expect(parseArgs(type, args)).toEqual({ values, extra: '--unknown 1' });
    }
  });
});

/**
 * The restic options a job form can build. A job stores its options as one string, split like a shell
 * would (see runner.SplitArgs in the daemon), so the form parses that string into fields and writes it back.
 * Options the form does not know stay untouched in `extra`.
 */
export type JobType = 'backup' | 'forget' | 'check';

export type ArgKind = 'bool' | 'text' | 'number' | 'list' | 'select';

export interface ArgSpec {
  flag: string;
  /** Short form such as `x` for `-x`, recognised when parsing. */
  short?: string;
  kind: ArgKind;
  label: string;
  hint?: string;
  placeholder?: string;
  /** Choices of a `select`, the first one is the default and writes nothing. */
  options?: { value: string; label: string }[];
}

export interface ArgGroup {
  title: string;
  description?: string;
  /** Lay the fields out side by side, for short numbers. */
  compact?: boolean;
  specs: ArgSpec[];
}

export type ArgValue = boolean | string | string[];
export type ArgValues = Record<string, ArgValue>;

export interface ParsedArgs {
  values: ArgValues;
  /** The arguments the form does not know, as text. */
  extra: string;
}

export const ARG_GROUPS: Record<JobType, ArgGroup[]> = {
  backup: [
    {
      title: 'Exclusions',
      description: 'Files and directories that are left out of the snapshot.',
      specs: [
        { flag: 'exclude', kind: 'list', label: 'Exclude patterns', placeholder: '*.tmp', hint: 'Glob patterns, matched against the whole path.' },
        { flag: 'iexclude', kind: 'list', label: 'Exclude patterns, ignoring case', placeholder: '*.TMP' },
        { flag: 'exclude-file', kind: 'list', label: 'Exclude pattern files', placeholder: '/.backupignore', hint: 'Files that list one pattern per line.' },
        { flag: 'exclude-caches', kind: 'bool', label: 'Exclude cache directories', hint: 'Skips directories that contain a CACHEDIR.TAG.' },
        { flag: 'exclude-if-present', kind: 'text', label: 'Exclude directories that contain a file', placeholder: '.nobackup' },
        { flag: 'exclude-larger-than', kind: 'text', label: 'Exclude files larger than', placeholder: '1G', hint: 'A size such as 500M or 2G.' },
      ],
    },
    {
      title: 'Behaviour',
      specs: [
        { flag: 'one-file-system', short: 'x', kind: 'bool', label: 'Stay on one file system', hint: 'Does not descend into mounted file systems.' },
        { flag: 'skip-if-unchanged', kind: 'bool', label: 'Skip the snapshot when nothing changed' },
        { flag: 'no-scan', kind: 'bool', label: 'Skip the scan before the backup', hint: 'Starts sooner, but there is no progress estimate.' },
        { flag: 'ignore-inode', kind: 'bool', label: 'Ignore inode changes', hint: 'For file systems that do not keep inodes stable.' },
        { flag: 'ignore-ctime', kind: 'bool', label: 'Ignore change times' },
        { flag: 'read-concurrency', kind: 'number', label: 'Files read in parallel', placeholder: '2', hint: 'Higher is faster on fast storage.' },
      ],
    },
    {
      title: 'Snapshot',
      specs: [
        { flag: 'tag', kind: 'list', label: 'Tags', placeholder: 'daily', hint: 'Labels that make snapshots easier to find.' },
        { flag: 'host', kind: 'text', label: 'Host name', placeholder: 'nas', hint: 'Overrides the host name stored in the snapshot.' },
      ],
    },
  ],
  forget: [
    {
      title: 'Keep',
      description: 'Which snapshots stay. A snapshot that matches any rule is kept, everything else is forgotten.',
      compact: true,
      specs: [
        { flag: 'keep-last', kind: 'number', label: 'Last', placeholder: '7' },
        { flag: 'keep-hourly', kind: 'number', label: 'Hourly', placeholder: '24' },
        { flag: 'keep-daily', kind: 'number', label: 'Daily', placeholder: '14' },
        { flag: 'keep-weekly', kind: 'number', label: 'Weekly', placeholder: '4' },
        { flag: 'keep-monthly', kind: 'number', label: 'Monthly', placeholder: '6' },
        { flag: 'keep-yearly', kind: 'number', label: 'Yearly', placeholder: '1' },
      ],
    },
    {
      title: 'More rules',
      specs: [
        { flag: 'keep-within', kind: 'text', label: 'Keep everything within', placeholder: '30d', hint: 'A duration such as 2y5m7d3h.' },
        { flag: 'keep-tag', kind: 'list', label: 'Always keep snapshots with the tag', placeholder: 'keep' },
      ],
    },
    {
      title: 'Selection',
      description: 'Which snapshots the rules apply to.',
      specs: [
        {
          flag: 'group-by',
          kind: 'select',
          label: 'Apply the rules per',
          options: [
            { value: '', label: 'Host and paths (default)' },
            { value: 'host', label: 'Host' },
            { value: 'paths', label: 'Paths' },
            { value: 'tags', label: 'Tags' },
            { value: 'host,tags', label: 'Host and tags' },
            { value: 'host,paths,tags', label: 'Host, paths and tags' },
          ],
        },
        { flag: 'tag', kind: 'list', label: 'Only snapshots with the tag', placeholder: 'daily' },
        { flag: 'host', kind: 'text', label: 'Only snapshots of the host', placeholder: 'nas' },
      ],
    },
    {
      title: 'Afterwards',
      specs: [
        { flag: 'prune', kind: 'bool', label: 'Remove the data nothing refers to anymore (prune)', hint: 'Frees the space, but takes a while and locks the repository.' },
        { flag: 'max-unused', kind: 'text', label: 'Tolerate unused data up to', placeholder: '5%', hint: 'A percentage or size, a larger limit means less rewriting.' },
        { flag: 'dry-run', short: 'n', kind: 'bool', label: 'Dry run', hint: 'Only reports what would be removed.' },
      ],
    },
  ],
  check: [
    {
      title: 'Verification',
      description: 'By default only the structure of the repository is checked.',
      specs: [
        { flag: 'read-data-subset', kind: 'text', label: 'Read part of the data', placeholder: '5%', hint: 'A percentage (5%), a fraction (1/10) or a size (2G). Each run can check another part.' },
        { flag: 'read-data', kind: 'bool', label: 'Read all data', hint: 'Slow and transfers the whole repository, but finds every damaged file.' },
        { flag: 'with-cache', kind: 'bool', label: 'Use the local cache', hint: 'Faster, but not as thorough.' },
        { flag: 'check-unused', kind: 'bool', label: 'Report unused data' },
      ],
    },
  ],
};

/** The specs of a type in the order they are written. */
export function specsOf(type: JobType): ArgSpec[] {
  return ARG_GROUPS[type].flatMap((group) => group.specs);
}

/**
 * Splits like the daemon does (runner.SplitArgs): quotes and backslashes work, nothing is expanded.
 * Returns null for an unterminated quote or a trailing backslash.
 */
export function splitArgs(input: string): string[] | null {
  const args: string[] = [];
  let current = '';
  let inArg = false;
  let quote = '';
  let escaped = false;
  for (const char of input) {
    if (escaped) {
      current += char;
      escaped = false;
    } else if (char === '\\' && quote !== "'") {
      escaped = true;
      inArg = true;
    } else if (quote) {
      if (char === quote) {
        quote = '';
      } else {
        current += char;
      }
    } else if (char === "'" || char === '"') {
      quote = char;
      inArg = true;
    } else if (char === ' ' || char === '\t' || char === '\n') {
      if (inArg) {
        args.push(current);
        current = '';
        inArg = false;
      }
    } else {
      current += char;
      inArg = true;
    }
  }
  if (quote || escaped) {
    return null;
  }
  if (inArg) {
    args.push(current);
  }
  return args;
}

/** Quotes an argument when it has to be, so that `splitArgs` gives it back unchanged. */
export function quoteArg(arg: string): string {
  if (arg !== '' && /^[A-Za-z0-9_@%+=:,./~^-]+$/.test(arg)) {
    return arg;
  }
  return `"${arg.replace(/(["\\])/g, '\\$1')}"`;
}

/** Reads the options the form knows out of an argument string, the rest ends up in `extra`. */
export function parseArgs(type: JobType, args: string): ParsedArgs {
  const tokens = splitArgs(args);
  if (!tokens) {
    // Not parseable, the user has to fix the quotes, nothing is lost.
    return { values: {}, extra: args };
  }
  const specs = specsOf(type);
  const byFlag = new Map(specs.map((spec) => [`--${spec.flag}`, spec]));
  const byShort = new Map(specs.filter((s) => s.short).map((spec) => [`-${spec.short}`, spec]));

  const values: ArgValues = {};
  const extra: string[] = [];
  for (let i = 0; i < tokens.length; i++) {
    const token = tokens[i];
    const equals = token.startsWith('--') ? token.indexOf('=') : -1;
    const name = equals > 0 ? token.slice(0, equals) : token;
    const inline = equals > 0 ? token.slice(equals + 1) : undefined;
    const spec = byFlag.get(name) ?? byShort.get(name);

    if (!spec) {
      extra.push(token);
      // An unknown option usually takes the next word as its value, keep the two together.
      if (inline === undefined && token.startsWith('--') && i + 1 < tokens.length && !tokens[i + 1].startsWith('-')) {
        extra.push(tokens[++i]);
      }
      continue;
    }
    if (spec.kind === 'bool') {
      values[spec.flag] = inline === undefined ? true : inline !== 'false';
      continue;
    }
    const value = inline ?? (i + 1 < tokens.length ? tokens[++i] : undefined);
    if (value === undefined) {
      extra.push(token);
      continue;
    }
    if (spec.kind === 'list') {
      values[spec.flag] = [...((values[spec.flag] as string[] | undefined) ?? []), value];
    } else {
      values[spec.flag] = value;
    }
  }
  return { values, extra: extra.map(quoteArg).join(' ') };
}

/** Writes the options of the form and the unknown rest back into one argument string. */
export function buildArgs(type: JobType, values: ArgValues, extra: string): string {
  const tokens: string[] = [];
  for (const spec of specsOf(type)) {
    const value = values[spec.flag];
    switch (spec.kind) {
      case 'bool':
        if (value === true) {
          tokens.push(`--${spec.flag}`);
        }
        break;
      case 'list':
        for (const item of (value as string[] | undefined) ?? []) {
          if (item.trim()) {
            tokens.push(`--${spec.flag}`, item.trim());
          }
        }
        break;
      default:
        if (typeof value === 'string' && value.trim()) {
          tokens.push(`--${spec.flag}`, value.trim());
        }
    }
  }
  const known = tokens.map(quoteArg).join(' ');
  return [known, extra.trim()].filter(Boolean).join(' ');
}

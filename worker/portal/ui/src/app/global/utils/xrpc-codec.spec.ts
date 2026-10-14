import { fromWire, methodTypes, toWire } from './xrpc-codec';

describe('xrpc codec with the generated schema', () => {
  it('maps names to field numbers, including nested messages', () => {
    expect(
      toWire('backup.api.v1.RestoreRequest', {
        snapshot: 'abc',
        target: '/restore/x',
        include: ['/a'],
        extra: undefined,
      }),
    ).toEqual({ 1: 'abc', 2: '/restore/x', 3: ['/a'] });
  });

  it('maps field numbers back to names, also in repeated messages', () => {
    expect(
      fromWire('backup.api.v1.ListRunsResponse', { 1: [{ 1: 'a', 2: 'backup' }, { 1: 'b' }] }),
    ).toEqual({ runs: [{ id: 'a', job: 'backup' }, { id: 'b' }] });
  });

  it('accepts name keys on decode', () => {
    expect(fromWire('backup.api.v1.Job', { name: 'backup', 6: { id: 'x' } })).toEqual({
      name: 'backup',
      active: { id: 'x' },
    });
  });

  it('keeps unknown keys and untyped values untouched', () => {
    expect(toWire('backup.api.v1.JobRequest', { job: 'a', extra: 1 })).toEqual({ 1: 'a', extra: 1 });
    expect(fromWire('backup.api.v1.JobRequest', { 1: 'a', 999: 1 })).toEqual({ job: 'a', 999: 1 });
    expect(fromWire(undefined, { 1: 'a' })).toEqual({ 1: 'a' });
    expect(fromWire('backup.api.v1.JobRequest', 5)).toBe(5);
  });

  it('describes the request and response type of a method', () => {
    expect(methodTypes('Control', 'Watch')).toEqual({
      input: 'backup.api.v1.WatchRequest',
      output: 'backup.api.v1.Event',
    });
    expect(methodTypes('Nope', 'Nope')).toBeUndefined();
  });
});

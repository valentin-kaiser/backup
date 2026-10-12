import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';

import { rpcCall, rpcFail, rpcResult } from '../../global/utils/api.testing';
import { AuditPage } from './audit.page';

const entry = (id: number, extra: object = {}) => ({
  id: String(id),
  time: '2026-10-12T08:00:00Z',
  ip: '10.0.0.5',
  action: 'start_job',
  target: 'photos',
  result: 'ok',
  ...extra,
});

const page = (entries: object[], nextId = 0) => rpcResult({ entries, nextId: String(nextId) });

describe('AuditPage', () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.useFakeTimers();
    fetchMock = vi.fn(async () => page([entry(2), entry(1, { action: 'login', target: '' })]));
    vi.stubGlobal('fetch', fetchMock);
    TestBed.configureTestingModule({
      providers: [
        provideRouter([]),
      ],
    });
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  async function create() {
    const fixture = TestBed.createComponent(AuditPage);
    fixture.detectChanges();
    await vi.advanceTimersByTimeAsync(0);
    fixture.detectChanges();
    return { fixture, page: fixture.componentInstance as any, el: fixture.nativeElement as HTMLElement };
  }

  /** The parameters of the calls to a method, field 1 is the filter, 2 the id to continue after, 3 the limit. */
  const calls = (method = 'AuditLog.ListAuditLog') =>
    fetchMock.mock.calls.map(rpcCall).filter((c) => c.method === method);

  it('shows the newest entries with the words for the actions', async () => {
    const { el } = await create();
    expect(calls().map((c) => c.params)).toEqual([{ 3: 100 }]);
    const rows = [...el.querySelectorAll('tbody tr')].map((r) => r.textContent!.replace(/\s+/g, ' ').trim());
    expect(rows[0]).toContain('Run job');
    expect(rows[0]).toContain('photos');
    expect(rows[0]).toContain('10.0.0.5');
    expect(rows[1]).toContain('Sign-in');
  });

  it('loads again with the filters, and searches only after typing has paused', async () => {
    const { fixture, page } = await create();
    page.action.set('restore');
    page.result.set('failed');
    fixture.detectChanges();
    await vi.advanceTimersByTimeAsync(0);
    expect(calls().at(-1)!.params).toEqual({ 1: { 1: 'restore', 2: 'failed' }, 3: 100 });

    const before = calls().length;
    page.typed('pho');
    page.typed('photos');
    await vi.advanceTimersByTimeAsync(100);
    expect(calls().length).toBe(before);
    await vi.advanceTimersByTimeAsync(300);
    expect(calls().length).toBe(before + 1);
    expect((calls().at(-1)!.params as any)[1][3]).toBe('photos');
  });

  it('filters by whole days: from the start of the first, up to the end of the last', async () => {
    const { fixture, page } = await create();
    page.from.set('2026-10-01');
    page.to.set('2026-10-02');
    fixture.detectChanges();
    await vi.advanceTimersByTimeAsync(0);
    const filter = (calls().at(-1)!.params as any)[1];
    expect(new Date(filter[4]).getTime()).toBe(new Date('2026-10-01T00:00:00').getTime());
    expect(new Date(filter[5]).getTime()).toBe(new Date('2026-10-03T00:00:00').getTime());
  });

  it('pages back with the id of the last entry', async () => {
    fetchMock.mockResolvedValueOnce(page([entry(30), entry(29)], 29));
    const { fixture, page: p, el } = await create();
    expect(el.textContent).toContain('Load older entries');
    fetchMock.mockResolvedValueOnce(page([entry(28)]));
    p.more();
    await vi.advanceTimersByTimeAsync(0);
    fixture.detectChanges();
    expect(calls().at(-1)!.params[2]).toBe(29);
    expect(el.querySelectorAll('tbody tr').length).toBe(3);
    expect(el.textContent).not.toContain('Load older entries');
  });

  it('exports what matches the filters, not only the page', async () => {
    const created = vi.fn(() => 'blob:audit');
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: created, revokeObjectURL: vi.fn() }));
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);
    const { fixture, page } = await create();
    page.action.set('start_job');
    fixture.detectChanges();
    await vi.advanceTimersByTimeAsync(0);
    fetchMock.mockResolvedValueOnce(rpcResult({ filename: 'audit-2026-10-12.csv', contentType: 'text/csv', data: btoa('id,time') }));
    await page.download('csv');
    expect(calls('AuditLog.ExportAuditLog').map((c) => c.params)).toEqual([{ 1: { 1: 'start_job' }, 2: 'csv' }]);
    expect(created).toHaveBeenCalled();
    expect(click).toHaveBeenCalled();
    click.mockRestore();
  });

  it('shows the error of the portal', async () => {
    fetchMock.mockResolvedValueOnce(rpcFail(500, 'reading the audit log failed'));
    const { el } = await create();
    expect(el.textContent).toContain('reading the audit log failed');
  });

  it('marks what did not work', async () => {
    fetchMock.mockResolvedValueOnce(
      page([entry(3, { result: 'failed', detail: 'job is already running' }), entry(2, { result: 'unknown' })]),
    );
    const { el } = await create();
    const chips = [...el.querySelectorAll('.chip')];
    expect(chips[0].className).toContain('failed');
    expect(chips[1].className).toContain('cancelled');
    expect(el.textContent).toContain('job is already running');
  });
});

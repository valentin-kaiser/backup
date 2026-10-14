import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { Router, provideRouter } from '@angular/router';

import { FileNode, FileType, Snapshot } from '../../global/model/backup/api/v1/control';
import { ListFilesResponse } from '../../global/model/backup/api/v1/control_service';
import { ControlService } from '../../global/services/control/control.service';
import { BrowsePage, breadcrumbs } from './browse.page';

const node = (name: string, path: string, type: FileType, size = 0) =>
  FileNode.create({ name, path, type, size, modifyTime: new Date('2024-01-02T03:04:05Z'), permissions: type === FileType.FILE_TYPE_DIRECTORY ? 'drwxr-xr-x' : '-rw-r--r--' });

describe('breadcrumbs', () => {
  it('turns a path into links', () => {
    expect(breadcrumbs('/')).toEqual([{ name: '/', path: '/' }]);
    expect(breadcrumbs('/a/b')).toEqual([
      { name: '/', path: '/' },
      { name: 'a', path: '/a' },
      { name: 'b', path: '/a/b' },
    ]);
  });
});

describe('BrowsePage', () => {
  let control: { loadFiles: any; loadSnapshots: any; defaults: any; restore: any };

  function create(inputs: Record<string, string> = {}) {
    const fixture = TestBed.createComponent(BrowsePage);
    fixture.componentRef.setInput('snapshot', 'abcdef0123456789');
    for (const [key, value] of Object.entries(inputs)) {
      fixture.componentRef.setInput(key, value);
    }
    fixture.detectChanges();
    return { fixture, page: fixture.componentInstance as any, element: fixture.nativeElement as HTMLElement };
  }
  const settle = async (fixture: { detectChanges: () => void; whenStable: () => Promise<unknown> }) => {
    await fixture.whenStable();
    fixture.detectChanges();
  };

  beforeEach(() => {
    control = {
      loadFiles: vi.fn(async (_job: string, _snapshot: string, path: string) =>
        ListFilesResponse.create({
          files:
            path === '/'
              ? [node('data', '/data', FileType.FILE_TYPE_DIRECTORY), node('a.txt', '/a.txt', FileType.FILE_TYPE_FILE, 1536)]
              : [node('b.txt', `${path}/b.txt`, FileType.FILE_TYPE_FILE, 12)],
        }),
      ),
      loadSnapshots: vi.fn(async () => [Snapshot.create({ id: 'abcdef0123456789', shortId: 'abcdef01', hostname: 'nas', paths: ['/data'], time: new Date() })]),
      defaults: signal({ restoreRoot: '/restore' }),
      restore: vi.fn(),
    };
    TestBed.configureTestingModule({ providers: [provideRouter([]), { provide: ControlService, useValue: control }] });
  });

  it('lists the root of the snapshot with sizes and the snapshot details', async () => {
    const { fixture, element } = create({ job: 'photos' });
    await settle(fixture);
    expect(control.loadFiles).toHaveBeenCalledWith('photos', 'abcdef0123456789', '/');
    const rows = Array.from(element.querySelectorAll('tbody tr')).map((r) => r.textContent?.replace(/\s+/g, ' ').trim());
    expect(rows[0]).toContain('data');
    expect(rows[1]).toContain('a.txt');
    expect(rows[1]).toContain('1.5 KiB');
    expect(element.querySelector('h1')?.textContent).toContain('abcdef01');
    expect(element.querySelector('header p')?.textContent).toContain('nas');
    // The root has no parent folder row.
    expect(element.querySelector('tr.up')).toBeNull();
  });

  it('reads the folder from the query string and shows where it is', async () => {
    const { fixture, element } = create({ job: 'photos', path: '/data/2024/' });
    await settle(fixture);
    expect(control.loadFiles).toHaveBeenLastCalledWith('photos', 'abcdef0123456789', '/data/2024');
    expect(Array.from(element.querySelectorAll('.crumbs .link')).map((b) => b.textContent?.trim())).toEqual(['/', 'data']);
    expect(element.querySelector('.crumbs .current')?.textContent).toBe('2024');
    expect(element.querySelector('tr.up')).not.toBeNull();
  });

  it('opens a folder by changing the path in the URL', async () => {
    const { fixture, element } = create({ job: 'photos' });
    await settle(fixture);
    const navigate = vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
    (element.querySelector('.name button') as HTMLElement).click();
    expect(navigate).toHaveBeenCalledWith([], { queryParams: { path: '/data' }, queryParamsHandling: 'merge' });
  });

  it('filters the entries of the folder', async () => {
    const { fixture, element } = create();
    await settle(fixture);
    const input = element.querySelector('#filter') as HTMLInputElement;
    input.value = 'a.t';
    input.dispatchEvent(new Event('input'));
    fixture.detectChanges();
    expect(element.querySelectorAll('tbody tr')).toHaveLength(1);
    input.value = 'zzz';
    input.dispatchEvent(new Event('input'));
    fixture.detectChanges();
    expect(element.querySelector('td.empty')?.textContent).toContain('matches the filter');
  });

  it('starts a restore of one entry or of the folder in view', async () => {
    const { fixture, page } = create({ job: 'photos', path: '/data' });
    await settle(fixture);
    const open = vi.fn();
    vi.spyOn(page, 'restoreDialog').mockReturnValue({ open } as any);
    page.restore(node('b.txt', '/data/b.txt', FileType.FILE_TYPE_FILE));
    expect(open).toHaveBeenLastCalledWith('photos', expect.objectContaining({ id: 'abcdef0123456789' }), '/data/b.txt');
    page.restore();
    expect(open).toHaveBeenLastCalledWith('photos', expect.anything(), '/data');
  });

  it('tells why a folder could not be read and tries again', async () => {
    control.loadFiles.mockRejectedValueOnce(new Error('repository query failed: no matching ID found'));
    const { fixture, element, page } = create();
    await settle(fixture);
    expect(element.querySelector('.banner.error')?.textContent).toContain('no matching ID found');
    page.reload();
    await settle(fixture);
    expect(element.querySelector('.banner.error')).toBeNull();
    expect(element.querySelectorAll('tbody tr').length).toBeGreaterThan(0);
  });

  it('warns when the folder was cut off', async () => {
    control.loadFiles.mockResolvedValueOnce(ListFilesResponse.create({ files: [node('a', '/a', FileType.FILE_TYPE_FILE)], truncated: true }));
    const { fixture, element } = create();
    await settle(fixture);
    expect(element.querySelector('.banner.warn')?.textContent).toContain('too large');
  });
});

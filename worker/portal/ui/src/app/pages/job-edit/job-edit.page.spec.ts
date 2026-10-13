import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';

import { Job, JobDefinition } from '../../global/model/backup/api/v1/control';
import { ControlService } from '../../global/services/control/control.service';
import { JobEditPage } from './job-edit.page';

const existing = Job.create({
  name: 'photos',
  type: 'backup',
  definition: JobDefinition.create({
    name: 'photos',
    type: 'backup',
    enabled: true,
    schedule: '0 1 * * *',
    args: '--exclude-caches',
    source: '/photos',
    repository: '/repo/photos',
  }),
});

describe('JobEditPage', () => {
  let control: {
    createJob: any;
    updateJob: any;
    deleteJob: any;
    loadSecrets: any;
    loadPeers: any;
    peers: any;
    secrets: any;
    status: any;
    definedJobs: any;
    jobs: any;
    defaults: any;
  };

  function create(name?: string) {
    const fixture = TestBed.createComponent(JobEditPage);
    if (name) {
      fixture.componentRef.setInput('name', name);
    }
    fixture.detectChanges();
    return { fixture, page: fixture.componentInstance as any, element: fixture.nativeElement as HTMLElement };
  }

  beforeEach(() => {
    control = {
      createJob: vi.fn(async () => undefined),
      updateJob: vi.fn(async () => undefined),
      deleteJob: vi.fn(async () => undefined),
      loadSecrets: vi.fn(async () => undefined),
      loadPeers: vi.fn(async () => undefined),
      peers: signal([]),
      secrets: signal([]),
      status: signal({}),
      definedJobs: signal([existing]),
      jobs: signal([existing, Job.create({ name: 'restore' })]),
      defaults: signal({ source: '/src', repository: '/repo', passwordFile: '', restoreRoot: '/restore' }),
    };
    TestBed.configureTestingModule({
      providers: [provideRouter([]), { provide: ControlService, useValue: control }],
    });
  });

  it('starts a new job with sensible values', () => {
    const { page } = create();
    expect(page.isNew()).toBe(true);
    expect(page.draft()).toMatchObject({ type: 'backup', enabled: true, schedule: '0 2 * * *', maxAgeHours: 26 });
    expect(page.draft().args).toContain('--exclude-caches');
  });

  it('validates the name, the schedule and the numbers', () => {
    const { page } = create();
    expect(page.valid()).toBe(false);
    expect(page.errors().name).toBeTruthy();

    page.set('name', 'My Job');
    expect(page.errors().name).toBeTruthy();
    page.set('name', 'restore');
    expect(page.errors().name).toBeTruthy();
    page.set('name', 'photos');
    expect(page.errors().name).toContain('taken');
    page.set('name', 'documents');
    expect(page.errors().name).toBeUndefined();

    page.set('schedule', 'every day');
    expect(page.errors().schedule).toBeTruthy();
    page.set('schedule', '0 3 * * *');
    expect(page.errors().schedule).toBeUndefined();

    page.setNumber('timeoutMinutes', '-5');
    expect(page.errors().timeout).toBeTruthy();
    page.setNumber('timeoutMinutes', '30');
    expect(page.valid()).toBe(true);
  });

  it('validates the paths of a sync job and saves its settings', async () => {
    control.peers.set([{ name: 'east' }]);
    const { page } = create();
    page.setType('sync');
    page.setEngine('rclone');
    page.set('name', 'mirror');
    expect(page.errors().path1).toBeTruthy();
    expect(page.errors().path2).toBeTruthy();

    page.set('path1', '/sync/a');
    page.set('path2', '/sync/a');
    expect(page.errors().path2).toContain('same');
    page.set('path2', '/sync/b');
    page.set('conflict', 'path1');
    page.set('conflictLoser', 'delete');
    page.set('checkAccess', true);
    page.set('force', true);
    page.setNumber('maxDeletePct', '0');
    expect(page.errors().maxDelete).toBeTruthy();
    page.setNumber('maxDeletePct', '20');
    expect(page.valid()).toBe(true);

    await page.save();
    const saved = control.createJob.mock.calls[0][0];
    expect(saved).toMatchObject({
      name: 'mirror',
      type: 'sync',
      path1: '/sync/a',
      path2: '/sync/b',
      conflict: 'path1',
      conflictLoser: 'delete',
      maxDeletePct: 20,
      checkAccess: true,
      force: true,
      repository: '',
      passwordSecret: '',
    });
  });

  it('syncs with unison by default and drops the settings only rclone has', async () => {
    const { page } = create();
    page.setType('sync');
    expect(page.draft().engine).toBe('unison');
    page.set('name', 'mirror');
    page.set('path1', '/sync/a');
    page.set('path2', '/sync/b');
    page.set('conflict', 'larger');
    page.set('force', true);
    page.setNumber('maxDeletePct', '0');
    expect(page.errors().maxDelete).toBeUndefined();
    page.setEngine('rclone');
    page.setEngine('unison');
    expect(page.draft().conflict).toBe('newer'); // unison cannot do larger
    await page.save();
    expect(control.createJob).toHaveBeenCalled();
    const saved = control.createJob.mock.calls[0][0];
    expect(saved).toMatchObject({ engine: 'unison', force: false, maxDeletePct: 0 });
  });

  it('replaces a conflict policy unison cannot do when the engine changes', () => {
    const { page } = create();
    page.setType('sync');
    page.setEngine('rclone');
    page.set('conflict', 'smaller');
    page.setEngine('unison');
    expect(page.draft().conflict).toBe('newer');
  });

  it('syncs with a peer and keeps the directory below its export', async () => {
    control.peers.set([{ name: 'east' }]);
    const { page } = create();
    page.setType('sync');
    page.set('name', 'shared');
    page.set('path1', '/sync');
    page.set('peer', 'west');
    expect(page.errors().path2).toBeTruthy();
    page.set('peer', 'east');
    page.set('peerDir', '/projects/');
    expect(page.valid()).toBe(true);
    await page.save();
    expect(control.createJob.mock.calls[0][0].path2).toBe('peer:east/projects');
  });

  it('needs members for a group, keeps their order and sends only group fields', async () => {
    const { page } = create();
    page.setType('group');
    page.set('name', 'nightly');
    expect(page.errors().members).toBeTruthy();

    page.addMember('backup');
    page.addMember('check');
    page.addMember('forget');
    page.moveMember(2, -1);
    page.moveMember(0, -1);
    page.removeMember(0);
    page.set('continueOnError', true);
    expect(page.draft().members).toEqual(['forget', 'check']);
    expect(page.valid()).toBe(true);

    await page.save();
    expect(control.createJob.mock.calls[0][0]).toMatchObject({
      name: 'nightly',
      type: 'group',
      members: ['forget', 'check'],
      continueOnError: true,
      args: '',
      repository: '',
    });
  });

  it('does not require a schedule for jobs that only run by hand', () => {
    const { page } = create();
    page.set('name', 'manual');
    page.set('enabled', false);
    page.set('schedule', '');
    expect(page.valid()).toBe(true);
  });

  it('swaps the suggested arguments with the type unless they were edited', () => {
    const { page } = create();
    page.setType('forget');
    expect(page.draft().args).toContain('--keep-last');

    page.set('args', '--keep-last 3');
    page.setType('check');
    expect(page.draft().args).toBe('--keep-last 3');
  });

  it('drops the source when the type is not backup', () => {
    const { page } = create();
    page.set('source', '/data');
    page.setType('check');
    expect(page.draft().source).toBe('');
  });

  it('creates the job and does not send an invalid one', async () => {
    const { page } = create();
    await page.save();
    expect(control.createJob).not.toHaveBeenCalled();
    expect(page.submitted()).toBe(true);

    page.set('name', 'documents');
    page.set('source', ' /docs ');
    await page.save();
    expect(control.createJob).toHaveBeenCalledTimes(1);
    expect(control.createJob.mock.calls[0][0]).toMatchObject({ name: 'documents', type: 'backup', source: '/docs', schedule: '0 2 * * *' });
  });

  it('shows the reason when the daemon refuses the job', async () => {
    control.createJob.mockRejectedValueOnce(new Error('job "documents" already exists'));
    const { page } = create();
    page.set('name', 'documents');
    await page.save();
    expect(page.error()).toBe('job "documents" already exists');
    expect(page.saving()).toBe(false);
  });

  it('loads an existing job once and keeps the name fixed', async () => {
    const { page, fixture, element } = create('photos');
    expect(page.isNew()).toBe(false);
    expect(page.draft()).toMatchObject({ name: 'photos', schedule: '0 1 * * *', source: '/photos', repository: '/repo/photos' });

    // A refresh of the status must not overwrite what the user is typing.
    page.set('args', '--exclude-caches --one-file-system');
    control.definedJobs.set([existing]);
    fixture.detectChanges();
    expect(page.draft().args).toBe('--exclude-caches --one-file-system');
    expect((element.querySelector('#name') as HTMLInputElement).disabled).toBe(true);

    await page.save();
    expect(control.updateJob).toHaveBeenCalledTimes(1);
    expect(control.updateJob.mock.calls[0][0]).toMatchObject({ name: 'photos', args: '--exclude-caches --one-file-system' });
  });

  it('reports a job that does not exist', () => {
    const { page } = create('gone');
    expect(page.notFound()).toBe(true);
  });
});

describe('JobEditPage stored password', () => {
  function setup() {
    const control: any = {
      createJob: vi.fn(async () => undefined),
      updateJob: vi.fn(async () => undefined),
      loadSecrets: vi.fn(async () => undefined),
      loadPeers: vi.fn(async () => undefined),
      peers: signal([]),
      secrets: signal([{ name: 'photos' }, { name: 'docs' }]),
      status: signal({}),
      definedJobs: signal([]),
      jobs: signal([]),
      defaults: signal({ source: '/src', repository: '/repo', passwordFile: '', restoreRoot: '/restore' }),
    };
    TestBed.configureTestingModule({ providers: [provideRouter([]), { provide: ControlService, useValue: control }] });
    const fixture = TestBed.createComponent(JobEditPage);
    fixture.detectChanges();
    return { control, fixture, page: fixture.componentInstance as any, el: fixture.nativeElement as HTMLElement };
  }

  it('offers the stored secrets', () => {
    const { control, el } = setup();
    expect(control.loadSecrets).toHaveBeenCalled();
    const select = el.querySelector<HTMLSelectElement>('#secret')!;
    expect([...select.options].map((o) => o.textContent!.trim())).toEqual(['None, use the password file', 'photos', 'docs']);
  });

  it('saves the secret and drops the password file, the secret replaces it', async () => {
    const { control, page, fixture } = setup();
    page.set('name', 'nightly');
    page.set('passwordFile', '/old/file');
    page.set('passwordSecret', 'photos');
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('#password').disabled).toBe(true);
    await page.save();
    const saved = control.createJob.mock.calls[0][0];
    expect(saved.passwordSecret).toBe('photos');
    expect(saved.passwordFile).toBe('');
  });

  it('keeps the password file when no secret is chosen', async () => {
    const { control, page } = setup();
    page.set('name', 'nightly');
    page.set('passwordFile', '/run/secrets/pw');
    await page.save();
    const saved = control.createJob.mock.calls[0][0];
    expect(saved.passwordFile).toBe('/run/secrets/pw');
    expect(saved.passwordSecret).toBe('');
  });
});

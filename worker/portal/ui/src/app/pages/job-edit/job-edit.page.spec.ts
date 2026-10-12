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
  let control: { createJob: any; updateJob: any; deleteJob: any; status: any; definedJobs: any; jobs: any; defaults: any };

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

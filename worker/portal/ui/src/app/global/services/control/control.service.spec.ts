import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { Subject } from 'rxjs';

import {
  ControlStatus,
  Event,
  EventType,
  Job,
  JobDefinition,
  RunStatus,
} from '../../model/backup/api/v1/control';
import { ListRunsResponse } from '../../model/backup/api/v1/control_service';
import { RpcService } from '../rpc/rpc.service';
import { ControlService } from './control.service';

const definition = (name: string, type: string, repository = ''): JobDefinition =>
  JobDefinition.create({ name, type, enabled: true, schedule: '0 0 * * *', repository });

const job = (name: string, type: string, repository = ''): Job =>
  Job.create({ name, type, enabled: true, schedule: '0 0 * * *', definition: definition(name, type, repository) });

describe('ControlService', () => {
  let control: ControlService;
  let stream: Subject<Event>;
  let calls: { method: string; params: any }[];
  let status: ControlStatus;

  beforeEach(() => {
    stream = new Subject<Event>();
    calls = [];
    status = ControlStatus.create({
      healthy: true,
      version: 'test',
      defaults: { source: '/src', repository: '/repo', passwordFile: '', restoreRoot: '/restore' },
      jobs: [
        job('photos', 'backup', '/repo/photos'),
        job('photos-prune', 'forget', '/repo/photos'),
        job('docs', 'backup'),
        job('docs-check', 'check'),
        Job.create({ name: 'restore' }),
      ],
    });
    const rpc = {
      connected: signal(true),
      connections: signal(0),
      call: vi.fn(async (_service: string, method: string, params: any) => {
        calls.push({ method, params });
        switch (method) {
          case 'GetStatus':
            return status;
          case 'ListRuns':
            return ListRunsResponse.create({ runs: [] });
          default:
            return Job.create();
        }
      }),
      stream: vi.fn(() => stream.asObservable()),
    };
    TestBed.configureTestingModule({ providers: [{ provide: RpcService, useValue: rpc }] });
    control = TestBed.inject(ControlService);
  });

  afterEach(() => control.stop());

  it('loads status and runs and knows the configured jobs', async () => {
    await control.refresh(true);
    expect(control.status()?.healthy).toBe(true);
    expect(control.jobs().map((j) => j.name)).toEqual(['photos', 'photos-prune', 'docs', 'docs-check', 'restore']);
    expect(control.definedJobs().map((j) => j.name)).toEqual(['photos', 'photos-prune', 'docs', 'docs-check']);
    expect(control.changes()).toBe(1);
  });

  it('only notifies the views when asked to, so polling does not reload them', async () => {
    await control.refresh(false);
    expect(control.changes()).toBe(0);
  });

  it('groups the jobs by repository and prefers a backup job to address it', async () => {
    await control.refresh(false);
    expect(control.repositories()).toEqual([
      { key: '/repo/photos', job: 'photos', jobs: ['photos', 'photos-prune'] },
      { key: '/repo', job: 'docs', jobs: ['docs', 'docs-check'] },
    ]);
  });

  it('flips the schedule of a job with the rest of its definition', async () => {
    await control.refresh(false);
    const photos = control.definedJobs()[0];
    await control.setEnabled(photos, false);
    const update = calls.find((c) => c.method === 'UpdateJob');
    expect(update?.params).toMatchObject({ name: 'photos', type: 'backup', repository: '/repo/photos', enabled: false });
  });

  it('reloads snapshots after a backup or a prune finished, not after a check', async () => {
    await control.refresh(false);
    control.start();
    const finished = (jobName: string) =>
      stream.next(
        Event.create({ type: EventType.EVENT_TYPE_STATUS, job: jobName, run: 'r', status: RunStatus.RUN_STATUS_SUCCESS }),
      );

    finished('docs-check');
    expect(control.repositoryChanges()).toBe(0);
    finished('photos');
    finished('photos-prune');
    expect(control.repositoryChanges()).toBe(2);

    stream.next(Event.create({ type: EventType.EVENT_TYPE_STATUS, job: 'photos', run: 'r', status: RunStatus.RUN_STATUS_RUNNING }));
    expect(control.repositoryChanges()).toBe(2);
  });

  it('keeps the output of the run that printed last', () => {
    control.start();
    stream.next(Event.create({ type: EventType.EVENT_TYPE_STATUS, job: 'photos', run: 'r', status: RunStatus.RUN_STATUS_RUNNING }));
    stream.next(Event.create({ type: EventType.EVENT_TYPE_LINE, job: 'photos', run: 'r', line: 'one' }));
    stream.next(Event.create({ type: EventType.EVENT_TYPE_LINE, job: 'photos', run: 'r', line: 'two' }));
    expect(control.liveJob()).toBe('photos');
    expect(control.liveLines()).toEqual(['one', 'two']);

    stream.next(Event.create({ type: EventType.EVENT_TYPE_STATUS, job: 'docs', run: 's', status: RunStatus.RUN_STATUS_RUNNING }));
    expect(control.liveLines()).toEqual([]);
  });

  it('publishes every event for the views that follow a single run', () => {
    control.start();
    const seen: string[] = [];
    control.events$.subscribe((e) => seen.push(e.line));
    stream.next(Event.create({ type: EventType.EVENT_TYPE_LINE, job: 'photos', run: 'r', line: 'hello' }));
    expect(seen).toEqual(['hello']);
  });
});

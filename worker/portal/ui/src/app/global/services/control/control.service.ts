import { Injectable, computed, effect, inject, signal, untracked } from '@angular/core';
import { Subject, Subscription, defer, repeat, retry } from 'rxjs';

import {
  ControlStatus,
  Event,
  EventType,
  Job,
  JobDefinition,
  RepositoryStats,
  Run,
  SecretInfo,
  RunStatus,
  Snapshot,
} from '../../model/backup/api/v1/control';
import {
  GetRunRequest,
  GetStatsRequest,
  ListFilesRequest,
  ListFilesResponse,
  JobRequest,
  ListRunsRequest,
  ListRunsResponse,
  ListSnapshotsRequest,
  ListSnapshotsResponse,
  ChangeRepositoryPasswordRequest,
  ChangeRepositoryPasswordResponse,
  ListSecretsRequest,
  ListSecretsResponse,
  RestoreRequest,
  SecretRequest,
  SetSecretRequest,
  SetSecretResponse,
  WatchRequest,
} from '../../model/backup/api/v1/control_service';
import { Empty } from '../../model/google/protobuf/empty';
import { isActive } from '../../utils/format';
import { RpcService } from '../rpc/rpc.service';
import { ToastService } from '../toast/toast.service';

const SERVICE = 'Control';
const RECENT_RUNS = 50;
const LIVE_LINES = 500;
const APPEND_KEY = 'portal.appendOutput';

function readAppend(): boolean {
  try {
    return localStorage.getItem(APPEND_KEY) === '1';
  } catch {
    return false;
  }
}
const REFRESH_DELAY_MS = 300;
/** Calls on the idle websocket also keep proxies from closing it. */
const POLL_MS = 30_000;
const RESUBSCRIBE_MS = 1_000;

/** A repository and the jobs that use it. */
export interface Repository {
  /** Repository location, the key of the entry. */
  key: string;
  /** A backup job that works on it, used to address the repository in the API. */
  job: string;
  /** All jobs that work on it. */
  jobs: string[];
}

export function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/**
 * The state of the daemon, kept current by its event stream. Pages read the signals and reload what they
 * show on their own when `changes` or `repositoryChanges` tick, nothing has to be refreshed by hand.
 */
@Injectable({ providedIn: 'root' })
export class ControlService {
  private readonly rpc = inject(RpcService);
  private readonly toast = inject(ToastService);

  readonly status = signal<ControlStatus | null>(null);
  /** The latest runs of all jobs. */
  readonly runs = signal<Run[]>([]);
  /** The last failed load, cleared by the next success. */
  readonly error = signal('');
  readonly connected = this.rpc.connected;

  /** All jobs including restore, which is not configurable. */
  readonly jobs = computed<Job[]>(() => this.status()?.jobs ?? []);
  /** The configured jobs. */
  readonly definedJobs = computed(() => this.jobs().filter((job) => job.definition));
  readonly defaults = computed(() => this.status()?.defaults);
  readonly running = computed(() => this.jobs().filter((job) => job.active));
  readonly repositories = computed<Repository[]>(() => {
    const fallback = this.defaults()?.repository ?? '';
    const found = new Map<string, Repository & { backup: boolean }>();
    for (const job of this.definedJobs()) {
      const key = job.definition?.repository || fallback;
      const entry = found.get(key) ?? { key, job: job.name, jobs: [], backup: false };
      entry.jobs.push(job.name);
      // Any job addresses the repository, a backup job is the most natural one.
      if (job.type === 'backup' && !entry.backup) {
        entry.job = job.name;
        entry.backup = true;
      }
      found.set(key, entry);
    }
    return [...found.values()].map(({ key, job, jobs }) => ({ key, job, jobs }));
  });
  /** The stored secrets (names only), loaded on demand. */
  readonly secrets = signal<SecretInfo[]>([]);
  /** Job and output lines of the run that printed last. */
  readonly liveJob = signal('');
  readonly liveLines = signal<string[]>([]);
  /** Keep the output of earlier runs and append new runs below it instead of clearing the box. */
  readonly appendOutput = signal(readAppend());

  /** Ticks when runs or jobs changed, history views reload on it. */
  readonly changes = signal(0);
  /** Ticks when a backup or forget run finished, snapshot views reload on it. */
  readonly repositoryChanges = signal(0);
  /** Every event of the stream, for views that follow a single run. */
  readonly events$ = new Subject<Event>();

  private readonly started = signal(false);
  private watching: Subscription | undefined;
  private refreshTimer: ReturnType<typeof setTimeout> | undefined;
  private pollTimer: ReturnType<typeof setInterval> | undefined;

  constructor() {
    // Events are lost while the connection is down, load the state again after every reconnect.
    effect(() => {
      const connections = this.rpc.connections();
      if (this.started() && connections > 0) {
        untracked(() => {
          void this.refresh(true);
          this.repositoryChanges.update((n) => n + 1);
        });
      }
    });
  }

  /** Starts following the daemon. */
  start(): void {
    if (this.started()) {
      return;
    }
    this.started.set(true);
    this.watching = defer(() => this.rpc.stream(SERVICE, 'Watch', WatchRequest.create(), Event))
      .pipe(retry({ delay: RESUBSCRIBE_MS }), repeat({ delay: RESUBSCRIBE_MS }))
      .subscribe((event) => this.onEvent(event));
    this.pollTimer = setInterval(() => void this.refresh(false), POLL_MS);
  }

  /** Forgets everything loaded, used when somebody signs out. */
  reset(): void {
    this.status.set(null);
    this.runs.set([]);
    this.error.set('');
    this.liveJob.set('');
    this.liveLines.set([]);
  }

  stop(): void {
    this.started.set(false);
    this.watching?.unsubscribe();
    this.watching = undefined;
    clearTimeout(this.refreshTimer);
    clearInterval(this.pollTimer);
  }

  /** Loads status and recent runs. `notify` lets the views reload as well. */
  async refresh(notify: boolean): Promise<void> {
    try {
      const [status, runs] = await Promise.all([
        this.rpc.call(SERVICE, 'GetStatus', {}, ControlStatus),
        this.rpc.call(SERVICE, 'ListRuns', ListRunsRequest.create({ limit: RECENT_RUNS }), ListRunsResponse),
      ]);
      this.status.set(status);
      this.runs.set(runs.runs);
      this.error.set('');
      if (notify) {
        this.changes.update((n) => n + 1);
      }
    } catch (error) {
      this.error.set(message(error));
    }
  }

  // ── Queries for the views ──────────────────────────────────────────────

  async loadRuns(job: string, limit: number): Promise<Run[]> {
    const response = await this.rpc.call(SERVICE, 'ListRuns', ListRunsRequest.create({ job, limit }), ListRunsResponse);
    return response.runs;
  }

  getRun(id: string): Promise<Run> {
    return this.rpc.call(SERVICE, 'GetRun', GetRunRequest.create({ id }), Run);
  }

  /** Lists the snapshots of the repository of a job, newest first. */
  async loadSnapshots(job: string): Promise<Snapshot[]> {
    const response = await this.rpc.call(SERVICE, 'ListSnapshots', ListSnapshotsRequest.create({ job }), ListSnapshotsResponse);
    return response.snapshots;
  }

  /** The entries directly inside a directory of a snapshot. A snapshot never changes, so this can be cached. */
  loadFiles(job: string, snapshot: string, path: string): Promise<ListFilesResponse> {
    return this.rpc.call(SERVICE, 'ListFiles', ListFilesRequest.create({ job, snapshot, path }), ListFilesResponse);
  }

  loadStats(job: string): Promise<RepositoryStats> {
    return this.rpc.call(SERVICE, 'GetStats', GetStatsRequest.create({ job }), RepositoryStats);
  }

  // ── Actions, failures are reported with a toast ────────────────────────

  startJob(job: string): Promise<boolean> {
    return this.act(`Started ${job}`, () => this.rpc.call(SERVICE, 'StartJob', JobRequest.create({ job }), Run));
  }

  cancelJob(job: string): Promise<boolean> {
    return this.act(`Cancelling ${job}`, () => this.rpc.call(SERVICE, 'CancelJob', JobRequest.create({ job }), Empty));
  }

  pauseJob(job: string): Promise<boolean> {
    return this.act(`Paused ${job}`, () => this.rpc.call(SERVICE, 'PauseJob', JobRequest.create({ job }), Empty));
  }

  resumeJob(job: string): Promise<boolean> {
    return this.act(`Resumed ${job}`, () => this.rpc.call(SERVICE, 'ResumeJob', JobRequest.create({ job }), Empty));
  }

  /** Turns the schedule of a job on or off. */
  setEnabled(job: Job, enabled: boolean): Promise<boolean> {
    const definition = job.definition;
    if (!definition) {
      return Promise.resolve(false);
    }
    return this.act(`${enabled ? 'Enabled' : 'Disabled'} ${job.name}`, () =>
      this.rpc.call(SERVICE, 'UpdateJob', JobDefinition.create({ ...definition, enabled }), Job),
    );
  }

  // ── Actions whose callers show the failure themselves, they throw ──────

  async createJob(definition: JobDefinition): Promise<void> {
    await this.rpc.call(SERVICE, 'CreateJob', definition, Job);
    this.toast.success(`Created ${definition.name}`);
    await this.refresh(true);
  }

  async updateJob(definition: JobDefinition): Promise<void> {
    await this.rpc.call(SERVICE, 'UpdateJob', definition, Job);
    this.toast.success(`Saved ${definition.name}`);
    await this.refresh(true);
  }

  async deleteJob(job: string): Promise<void> {
    await this.rpc.call(SERVICE, 'DeleteJob', JobRequest.create({ job }), Empty);
    this.toast.success(`Deleted ${job}`);
    await this.refresh(true);
  }

  // ── Secrets ──────────────────────────────────────────────────────────────

  async loadSecrets(): Promise<void> {
    try {
      this.secrets.set((await this.rpc.call(SERVICE, 'ListSecrets', ListSecretsRequest.create(), ListSecretsResponse)).secrets);
    } catch (error) {
      this.error.set(message(error));
    }
  }

  /** Stores a secret. Returns the generated value when one was asked for, it is not shown again. */
  async setSecret(name: string, value: string, generate: boolean): Promise<string> {
    const res = await this.rpc.call(SERVICE, 'SetSecret', SetSecretRequest.create({ name, value, generate }), SetSecretResponse);
    await this.loadSecrets();
    return res.generatedValue;
  }

  /** Changes the password of the repositories that use the secret. */
  async changeRepositoryPassword(
    name: string,
    value: string,
    generate: boolean,
  ): Promise<ChangeRepositoryPasswordResponse> {
    const res = await this.rpc.call(
      SERVICE,
      'ChangeRepositoryPassword',
      ChangeRepositoryPasswordRequest.create({ name, value, generate }),
      ChangeRepositoryPasswordResponse,
    );
    await this.loadSecrets();
    return res;
  }

  async deleteSecret(name: string): Promise<void> {
    await this.rpc.call(SERVICE, 'DeleteSecret', SecretRequest.create({ name }), Empty);
    this.toast.success(`Deleted ${name}`);
    await this.loadSecrets();
  }

  async restore(request: Partial<RestoreRequest>): Promise<Run> {
    const run = await this.rpc.call(SERVICE, 'Restore', RestoreRequest.create(request), Run);
    this.toast.success('Restore started');
    await this.refresh(true);
    return run;
  }

  private async act(done: string, call: () => Promise<unknown>): Promise<boolean> {
    try {
      await call();
      this.toast.success(done);
      await this.refresh(true);
      return true;
    } catch (error) {
      this.toast.error(message(error));
      await this.refresh(true);
      return false;
    }
  }

  setAppendOutput(on: boolean): void {
    this.appendOutput.set(on);
    try {
      localStorage.setItem(APPEND_KEY, on ? '1' : '0');
    } catch {
      // not persisted
    }
  }

  private onEvent(event: Event): void {
    this.events$.next(event);
    if (event.type === EventType.EVENT_TYPE_LINE) {
      this.liveJob.set(event.job);
      this.liveLines.update((lines) => [...lines.slice(1 - LIVE_LINES), event.line]);
      return;
    }
    if (event.status === RunStatus.RUN_STATUS_RUNNING) {
      this.liveJob.set(event.job);
      if (this.appendOutput() && this.liveLines().length) {
        this.liveLines.update((lines) => [...lines.slice(2 - LIVE_LINES), `── ${event.job} ──`]);
      } else {
        this.liveLines.set([]);
      }
    }
    if (!isActive(event.status) && this.changesRepository(event.job)) {
      this.repositoryChanges.update((n) => n + 1);
    }
    clearTimeout(this.refreshTimer);
    this.refreshTimer = setTimeout(() => void this.refresh(true), REFRESH_DELAY_MS);
  }

  /** Backups and prunes change the snapshots of a repository, checks and restores do not. */
  private changesRepository(job: string): boolean {
    const type = this.jobs().find((j) => j.name === job)?.type;
    return type === 'backup' || type === 'forget';
  }
}

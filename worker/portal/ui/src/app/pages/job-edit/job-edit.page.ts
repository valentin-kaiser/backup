import { ChangeDetectionStrategy, Component, computed, effect, inject, input, signal } from '@angular/core';
import { Router, RouterLink } from '@angular/router';

import { CronBuilderComponent } from '../../components/cron-builder/cron-builder.component';
import { IconComponent } from '../../components/icon/icon.component';
import { ResticArgsBuilderComponent } from '../../components/restic-args-builder/restic-args-builder.component';
import { JobDefinition } from '../../global/model/backup/api/v1/control';
import { ConfirmService } from '../../global/services/confirm/confirm.service';
import { ControlService, message } from '../../global/services/control/control.service';
import { validateCron } from '../../global/utils/cron';
import { JOB_TYPES } from '../../global/utils/format';
import { JobType } from '../../global/utils/restic-args';

/** The values of the form, numbers stay numbers until they are sent. */
interface Draft {
  name: string;
  type: string;
  enabled: boolean;
  schedule: string;
  args: string;
  timeoutMinutes: number;
  maxAgeHours: number;
  source: string;
  repository: string;
  passwordFile: string;
}

/** Arguments that make sense for a type, offered instead of an empty field. */
const SUGGESTED_ARGS: Record<string, string> = {
  backup: '--exclude-caches --exclude-if-present=.nobackup',
  forget: '--prune --keep-last 7 --keep-daily 14 --keep-weekly 4 --keep-monthly 6 --keep-yearly 1',
  check: '--read-data-subset=5%',
};

const NAME = /^[a-z0-9][a-z0-9_-]{0,31}$/;

function blank(type = 'backup'): Draft {
  return {
    name: '',
    type,
    enabled: true,
    schedule: '0 2 * * *',
    args: SUGGESTED_ARGS[type],
    timeoutMinutes: 0,
    maxAgeHours: type === 'backup' ? 26 : 0,
    source: '',
    repository: '',
    passwordFile: '',
  };
}

function fromDefinition(d: JobDefinition): Draft {
  return {
    name: d.name,
    type: d.type,
    enabled: d.enabled,
    schedule: d.schedule,
    args: d.args,
    timeoutMinutes: d.timeoutMinutes,
    maxAgeHours: d.maxAgeHours,
    source: d.source,
    repository: d.repository,
    passwordFile: d.passwordFile,
  };
}

/** Creates a job, or edits the job named in the route. */
@Component({
  selector: 'app-job-edit',
  imports: [RouterLink, IconComponent, CronBuilderComponent, ResticArgsBuilderComponent],
  templateUrl: './job-edit.page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class JobEditPage {
  protected readonly control = inject(ControlService);
  private readonly router = inject(Router);
  private readonly confirm = inject(ConfirmService);

  protected readonly types = JOB_TYPES;

  /** The name from the route, empty when a job is created. */
  readonly name = input<string | undefined>(undefined);

  protected readonly isNew = computed(() => !this.name());
  protected readonly draft = signal<Draft>(blank());
  protected readonly submitted = signal(false);
  protected readonly saving = signal(false);
  protected readonly error = signal('');
  private loadedFor: string | null = null;

  protected readonly notFound = computed(
    () => !this.isNew() && !!this.control.status() && !this.control.definedJobs().some((job) => job.name === this.name()),
  );
  protected readonly running = computed(() => this.control.jobs().some((job) => job.name === this.name() && job.active));

  protected readonly typeHint = computed(() => this.types.find((t) => t.value === this.draft().type)?.hint ?? '');
  protected readonly jobType = computed(() => this.draft().type as JobType);

  protected readonly errors = computed(() => {
    const d = this.draft();
    const errors: Partial<Record<'name' | 'schedule' | 'timeout' | 'maxAge', string>> = {};
    if (this.isNew()) {
      if (!NAME.test(d.name)) {
        errors.name = 'Use 1-32 lower case letters, digits, - or _, starting with a letter or digit';
      } else if (d.name === 'restore' || this.control.jobs().some((job) => job.name === d.name)) {
        errors.name = 'This name is already taken';
      }
    }
    if (d.enabled || d.schedule.trim()) {
      const problem = validateCron(d.schedule);
      if (problem) {
        errors.schedule = problem;
      }
    }
    if (!Number.isInteger(d.timeoutMinutes) || d.timeoutMinutes < 0) {
      errors.timeout = 'Enter 0 or a positive number of minutes';
    }
    if (!Number.isInteger(d.maxAgeHours) || d.maxAgeHours < 0) {
      errors.maxAge = 'Enter 0 or a positive number of hours';
    }
    return errors;
  });
  protected readonly valid = computed(() => Object.keys(this.errors()).length === 0);

  /** Advanced settings are shown when the job uses any of them. */
  protected readonly advancedOpen = computed(() => {
    const d = this.draft();
    return !!(d.repository || d.passwordFile || d.timeoutMinutes);
  });

  constructor() {
    // Fill the form once per job, later refreshes of the status must not overwrite what is being typed.
    effect(() => {
      const name = this.name() ?? '';
      if (this.loadedFor === name) {
        return;
      }
      if (!name) {
        this.loadedFor = name;
        this.draft.set(blank());
        return;
      }
      const definition = this.control.definedJobs().find((job) => job.name === name)?.definition;
      if (definition) {
        this.loadedFor = name;
        this.draft.set(fromDefinition(definition));
      }
    });
  }

  protected set<K extends keyof Draft>(key: K, value: Draft[K]): void {
    this.draft.update((draft) => ({ ...draft, [key]: value }));
  }

  protected setNumber(key: 'timeoutMinutes' | 'maxAgeHours', value: string): void {
    this.set(key, value.trim() === '' ? 0 : Number(value));
  }

  /** Switching the type replaces untouched arguments with the ones that fit the new type. */
  protected setType(type: string): void {
    this.draft.update((draft) => {
      const untouched = !draft.args.trim() || draft.args === SUGGESTED_ARGS[draft.type];
      return {
        ...draft,
        type,
        args: untouched ? SUGGESTED_ARGS[type] : draft.args,
        source: type === 'backup' ? draft.source : '',
      };
    });
  }

  protected async save(): Promise<void> {
    this.submitted.set(true);
    this.error.set('');
    if (!this.valid()) {
      return;
    }
    this.saving.set(true);
    const d = this.draft();
    const definition = JobDefinition.create({
      name: d.name.trim(),
      type: d.type,
      enabled: d.enabled,
      schedule: d.schedule.trim(),
      args: d.args.trim(),
      timeoutMinutes: d.timeoutMinutes,
      maxAgeHours: d.maxAgeHours,
      source: d.type === 'backup' ? d.source.trim() : '',
      repository: d.repository.trim(),
      passwordFile: d.passwordFile.trim(),
    });
    try {
      await (this.isNew() ? this.control.createJob(definition) : this.control.updateJob(definition));
      await this.router.navigate(['/jobs']);
    } catch (error) {
      this.error.set(message(error));
    } finally {
      this.saving.set(false);
    }
  }

  protected async remove(): Promise<void> {
    const name = this.name();
    if (!name) {
      return;
    }
    const confirmed = await this.confirm.ask({
      title: `Delete ${name}?`,
      message: 'The job is removed from the configuration and no longer runs. Its history and the snapshots in the repository are kept.',
      action: 'Delete',
      danger: true,
    });
    if (!confirmed) {
      return;
    }
    try {
      await this.control.deleteJob(name);
      await this.router.navigate(['/jobs']);
    } catch (error) {
      this.error.set(message(error));
    }
  }
}

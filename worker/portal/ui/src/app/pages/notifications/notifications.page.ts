import { ChangeDetectionStrategy, Component, computed, inject, signal } from '@angular/core';

import { IconComponent } from '../../components/icon/icon.component';
import { Notifications } from '../../global/model/backup/api/v1/control';
import { ControlService, message } from '../../global/services/control/control.service';
import { ToastService } from '../../global/services/toast/toast.service';

const ADDRESS = /^[^\s@<>,;]+@[^\s@<>,;]+\.[^\s@<>,;]+$/;

type SwitchKey = 'onFailure' | 'onSuccess' | 'onSkipped' | 'onStale' | 'onSecurity' | 'onLifecycle';

interface Switch {
  key: SwitchKey;
  title: string;
  hint: string;
}

const SWITCHES: Switch[] = [
  { key: 'onFailure', title: 'A job failed', hint: 'With the exit code and the last lines of its output.' },
  { key: 'onSuccess', title: 'A job succeeded', hint: 'Can be a lot of mail for jobs that run often.' },
  { key: 'onSkipped', title: 'A run was skipped or cancelled', hint: 'For example when the repository is not mounted or a peer is busy.' },
  { key: 'onStale', title: 'A backup is overdue', hint: 'A job has no successful run within its max age. Sent once, repeated daily.' },
  { key: 'onSecurity', title: 'Wrong passwords at the control API', hint: 'When an address is locked after too many attempts.' },
  { key: 'onLifecycle', title: 'The daemon starts or stops', hint: '' },
];

/** Which mails this worker sends and through which SMTP server. The portal mails through the same server. */
@Component({
  selector: 'app-notifications',
  imports: [IconComponent],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <header class="page-header">
      <div>
        <h1>Notifications</h1>
        <p class="muted">Mails about job results, overdue backups and security events of this worker.</p>
      </div>
    </header>

    @if (error()) {
      <div class="banner error" role="alert"><app-icon name="warning" /> {{ error() }}</div>
    }

    @if (draft(); as d) {
      @if (!d.transportConfigured && d.transportEditable) {
        <div class="banner error" role="status">
          <app-icon name="warning" />
          <span>No SMTP server is configured yet, so no mail can be sent. Fill in the server below.</span>
        </div>
      } @else if (!d.transportConfigured) {
        <div class="banner error" role="status">
          <app-icon name="warning" />
          <span>
            No SMTP server is configured, so no mail can be sent. Set <span class="mono">notify.mail</span> (host, port, from) in
            <span class="mono">control.yaml</span> or the <span class="mono">CONTROL_NOTIFY_MAIL_*</span> variables. The server and its
            password are not editable here.
          </span>
        </div>
      }

      <section class="card">
        <form class="form" (submit)="$event.preventDefault(); save()" novalidate>
          <label class="check">
            <input #en type="checkbox" [checked]="d.enabled" (change)="set({ enabled: en.checked })" />
            <span><strong>Send mail notifications</strong><span class="hint">Nothing is sent while this is off.</span></span>
          </label>

          @if (d.transportEditable) {
            <h3>Mail server</h3>
            <div class="grid">
              <div class="field">
                <label for="n-host">SMTP host</label>
                <input #sh id="n-host" class="mono" [value]="d.smtpHost" autocomplete="off" placeholder="smtp.example.com" (input)="set({ smtpHost: sh.value })" />
              </div>
              <div class="field">
                <label for="n-port">Port</label>
                <input #sp id="n-port" type="number" min="1" max="65535" [value]="d.smtpPort || ''" placeholder="587" (input)="set({ smtpPort: +sp.value })" />
              </div>
              <div class="field">
                <label for="n-from">Sender address</label>
                <input #sf id="n-from" class="mono" [value]="d.smtpFrom" autocomplete="off" placeholder="backup@example.com" (input)="set({ smtpFrom: sf.value })" />
              </div>
              <div class="field">
                <label for="n-enc">Encryption</label>
                <select #se id="n-enc" [value]="d.smtpEncryption || 'NONE'" (change)="set({ smtpEncryption: se.value })">
                  <option value="NONE">None</option>
                  <option value="STARTTLS">STARTTLS</option>
                  <option value="TLS">TLS</option>
                </select>
              </div>
            </div>
            <label class="check">
              <input #sa type="checkbox" [checked]="d.smtpAuth" (change)="set({ smtpAuth: sa.checked })" />
              <span><strong>The server wants a login</strong></span>
            </label>
            @if (d.smtpAuth) {
              <div class="grid">
                <div class="field">
                  <label for="n-user">Username</label>
                  <input #su id="n-user" class="mono" [value]="d.smtpUsername" autocomplete="off" (input)="set({ smtpUsername: su.value })" />
                </div>
                <div class="field">
                  <label for="n-pass">Password</label>
                  <input
                    #spw
                    id="n-pass"
                    type="password"
                    autocomplete="new-password"
                    [value]="d.smtpPassword"
                    [placeholder]="d.smtpPasswordSet ? 'Stored, leave empty to keep it' : ''"
                    (input)="set({ smtpPassword: spw.value, clearSmtpPassword: false })"
                  />
                  <span class="hint">Stored encrypted as the secret smtp_password, never shown again.</span>
                </div>
                <div class="field">
                  <label for="n-method">Login method</label>
                  <select #sm id="n-method" [value]="d.smtpAuthMethod || 'PLAIN'" (change)="set({ smtpAuthMethod: sm.value })">
                    <option value="PLAIN">PLAIN</option>
                    <option value="LOGIN">LOGIN</option>
                    <option value="CRAMMD5">CRAM-MD5</option>
                  </select>
                </div>
              </div>
            } @else if (d.smtpPasswordSet) {
              <label class="check">
                <input #cp type="checkbox" [checked]="d.clearSmtpPassword" (change)="set({ clearSmtpPassword: cp.checked })" />
                <span><strong>Remove the stored password</strong></span>
              </label>
            }
            <label class="check">
              <input #sk type="checkbox" [checked]="d.smtpSkipVerify" (change)="set({ smtpSkipVerify: sk.checked })" />
              <span><strong>Accept any certificate</strong><span class="hint">Only for a server with a self-signed certificate you trust.</span></span>
            </label>
          } @else if (d.transportConfigured) {
            <p class="muted">
              Sent through <strong class="mono">{{ d.smtpHost }}{{ d.smtpPort ? ':' + d.smtpPort : '' }}</strong> as
              <strong class="mono">{{ d.smtpFrom }}</strong>.
            </p>
          }

          <div class="grid">
            <div class="field">
              <label for="n-to">Recipients</label>
              <textarea
                #to
                id="n-to"
                class="mono"
                rows="4"
                [value]="recipientText()"
                autocomplete="off"
                placeholder="ops@example.com"
                (input)="recipientText.set(to.value)"
              ></textarea>
              @if (submitted() && badAddresses().length) {
                <span class="field-error">Not a mail address: {{ badAddresses().join(', ') }}</span>
              } @else {
                <span class="hint">One address per line, or separated by commas.</span>
              }
            </div>
            <div class="field">
              <label for="n-prefix">Subject prefix</label>
              <input #pf id="n-prefix" [value]="d.subjectPrefix" autocomplete="off" (input)="set({ subjectPrefix: pf.value })" />
            </div>
            <div class="field">
              <label for="n-cool">Minutes between two mails about the same job</label>
              <input #cd id="n-cool" type="number" min="0" [value]="d.cooldownMinutes" (input)="set({ cooldownMinutes: +cd.value })" />
              <span class="hint">0 sends every mail. A failure always gets through after a success.</span>
            </div>
          </div>

          <h3>Send a mail when</h3>
          @for (s of switches; track s.key) {
            <label class="check">
              <input #sw type="checkbox" [checked]="d[s.key]" (change)="setSwitch(s.key, sw.checked)" />
              <span>
                <strong>{{ s.title }}</strong>
                @if (s.hint) {
                  <span class="hint">{{ s.hint }}</span>
                }
              </span>
            </label>
          }
          @if (d.transportEditable) {
            <p class="muted">The portal hands its security events (failed sign-ins, refused calls) to this worker, so they use the same server and recipients.</p>
          }

          <div class="form-actions">
            <button
              type="button"
              class="ghost"
              [disabled]="busy() || dirty() || !d.enabled || !d.transportConfigured"
              [title]="dirty() ? 'Save first, the test uses the saved settings' : ''"
              (click)="test()"
            >
              <app-icon name="mail" /> Send test mail
            </button>
            <span class="spacer"></span>
            <button type="submit" class="primary" [disabled]="busy() || !dirty()">{{ busy() ? 'Saving…' : 'Save' }}</button>
          </div>
        </form>
      </section>
    } @else if (!error()) {
      <div class="skeleton cards-skeleton"></div>
    }
  `,
})
export class NotificationsPage {
  private readonly control = inject(ControlService);
  private readonly toast = inject(ToastService);

  protected readonly switches = SWITCHES;
  protected readonly draft = signal<Notifications | null>(null);
  protected readonly recipientText = signal('');
  protected readonly submitted = signal(false);
  protected readonly busy = signal(false);
  protected readonly error = signal('');
  private readonly saved = signal('');

  protected readonly addresses = computed(() =>
    this.recipientText()
      .split(/[\s,;]+/)
      .map((a) => a.trim())
      .filter(Boolean),
  );
  protected readonly badAddresses = computed(() => this.addresses().filter((a) => !ADDRESS.test(a)));
  protected readonly dirty = computed(() => this.key(this.draft(), this.addresses()) !== this.saved());

  constructor() {
    void this.load();
  }

  private key(n: Notifications | null, recipients: string[]): string {
    return n ? JSON.stringify({ ...n, recipients }) : '';
  }

  private apply(n: Notifications): void {
    this.draft.set(n);
    this.recipientText.set(n.recipients.join('\n'));
    this.saved.set(this.key(n, n.recipients));
  }

  private async load(): Promise<void> {
    try {
      this.apply(await this.control.loadNotifications());
    } catch (e) {
      this.error.set(message(e));
    }
  }

  protected set(change: Partial<Notifications>): void {
    this.draft.update((d) => (d ? { ...d, ...change } : d));
  }

  protected setSwitch(key: SwitchKey, value: boolean): void {
    this.set({ [key]: value });
  }

  protected async save(): Promise<void> {
    const d = this.draft();
    this.submitted.set(true);
    this.error.set('');
    if (!d || this.badAddresses().length) {
      return;
    }
    this.busy.set(true);
    try {
      this.apply(await this.control.saveNotifications({ ...d, recipients: this.addresses() }));
      this.submitted.set(false);
    } catch (e) {
      this.error.set(message(e));
    } finally {
      this.busy.set(false);
    }
  }

  protected async test(): Promise<void> {
    this.busy.set(true);
    this.error.set('');
    try {
      const to = await this.control.testNotifications();
      this.toast.success(`Test mail sent to ${to.join(', ')}`);
    } catch (e) {
      this.error.set(message(e));
    } finally {
      this.busy.set(false);
    }
  }
}

import { ChangeDetectionStrategy, Component, computed, inject, signal } from '@angular/core';

import { IconComponent } from '../../components/icon/icon.component';
import { Peer, PeerStatus, SyncServe } from '../../global/model/backup/api/v1/control';
import { ConfirmService } from '../../global/services/confirm/confirm.service';
import { ControlService, message } from '../../global/services/control/control.service';
import { ToastService } from '../../global/services/toast/toast.service';

const NAME = /^[a-z0-9][a-z0-9_]{0,31}$/;

function blankPeer(): Peer {
  return Peer.create({ username: 'sync' });
}

/** The peers a sync job can use, and how this worker serves its own directory to them. */
@Component({
  selector: 'app-peers',
  imports: [IconComponent],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <header class="page-header">
      <div>
        <h1>Sync</h1>
        <p class="muted">
          Peers are other workers that a sync job can keep a directory in sync with. Each worker serves one directory to its peers and
          lists the peers it connects to. This worker is called <strong class="mono">{{ control.workerName() || '…' }}</strong>, its
          peers have to use that name for it.
        </p>
      </div>
    </header>

    @if (error()) {
      <div class="banner error" role="alert"><app-icon name="warning" /> {{ error() }}</div>
    }

    <section class="card">
      <h2>Directory served to peers</h2>
      @if (serve(); as s) {
        <form class="form" (submit)="$event.preventDefault(); saveServe()" novalidate>
          <label class="check">
            <input #en type="checkbox" [checked]="s.enabled" (change)="setServe({ enabled: en.checked })" />
            <span><strong>Serve a directory to peers</strong><span class="hint">Needed on the worker whose volume a peer syncs with.</span></span>
          </label>
          <div class="grid">
            <div class="field">
              <label for="s-path">Directory</label>
              <input #sp id="s-path" class="mono" [value]="s.path" autocomplete="off" (input)="setServe({ path: sp.value })" />
            </div>
            <div class="field">
              <label for="s-address">Address</label>
              <input #sa id="s-address" class="mono" [value]="s.address" autocomplete="off" (input)="setServe({ address: sa.value })" />
            </div>
            <div class="field">
              <label for="s-port">Port</label>
              <input #sn id="s-port" type="number" min="1" max="65535" [value]="s.port" (input)="setServe({ port: +sn.value })" />
            </div>
            <div class="field">
              <label for="s-user">User name</label>
              <input #su id="s-user" [value]="s.username" autocomplete="off" (input)="setServe({ username: su.value })" />
            </div>
            <div class="field">
              <label for="s-secret">Password</label>
              <select #ss id="s-secret" (change)="setServe({ passwordSecret: ss.value, passwordFile: ss.value ? '' : s.passwordFile })">
                <option value="" [selected]="!s.passwordSecret">None, use the password file</option>
                @for (secret of control.secrets(); track secret.name) {
                  <option [value]="secret.name" [selected]="secret.name === s.passwordSecret">{{ secret.name }}</option>
                }
              </select>
              <span class="hint">A stored secret, created under Secrets. Peers use it as password.</span>
            </div>
            <div class="field">
              <label for="s-file">Password file</label>
              <input #sf id="s-file" class="mono" [value]="s.passwordFile" [disabled]="!!s.passwordSecret" autocomplete="off" (input)="setServe({ passwordFile: sf.value })" />
            </div>
            <div class="field">
              <label for="s-cert">Certificate file</label>
              <input #sc id="s-cert" class="mono" [value]="s.certificateFile" autocomplete="off" (input)="setServe({ certificateFile: sc.value })" />
              <span class="hint">Empty serves plain HTTP, only do that on a trusted network.</span>
            </div>
            <div class="field">
              <label for="s-key">Key file</label>
              <input #sk id="s-key" class="mono" [value]="s.keyFile" autocomplete="off" (input)="setServe({ keyFile: sk.value })" />
            </div>
          </div>
          <label class="check">
            <input #tn type="checkbox" [checked]="s.tunnel" (change)="setServe({ tunnel: tn.checked })" />
            <span>
              <strong>Let peers sync with Unison through the control API</strong>
              <span class="hint">
                No extra port or ssh needed. A peer that has the password of this worker's API can then read and write everything this
                worker can, not only the directory above. Only a peer that holds a reserved job gets a tunnel.
              </span>
            </span>
          </label>
          <div class="grid" hidden>
          </div>
          <div class="form-actions">
            <span class="spacer"></span>
            <button type="submit" class="primary" [disabled]="busy()">Save</button>
          </div>
        </form>
      } @else {
        <div class="skeleton cards-skeleton"></div>
      }
    </section>

    <section class="card">
      <div class="section-header">
        <h2>Peers</h2>
        <button type="button" class="primary small" (click)="add()"><app-icon name="add" /> Add peer</button>
      </div>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Sync server</th>
              <th>Control API</th>
              <th>Status</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            @for (p of control.peers(); track p.name) {
              <tr>
                <td class="strong">{{ p.name }}</td>
                <td class="mono">{{ p.url }}</td>
                <td class="mono">{{ p.apiUrl || '–' }}</td>
                <td>
                  @if (testing() === p.name) {
                    <span class="muted">Testing…</span>
                  } @else if (results()[p.name]; as r) {
                    <span [class.ok-text]="!r.error" [class.danger-text]="!!r.error" [title]="r.error">
                      {{ r.error ? 'Problem' : 'Connected' }}
                      @if (r.latencyMs) {
                        <small class="muted">{{ r.latencyMs }} ms</small>
                      }
                    </span>
                    @if (r.error) {
                      <div class="muted">{{ r.error }}</div>
                    }
                  } @else {
                    <span class="muted">Not tested</span>
                  }
                </td>
                <td class="right">
                  <button type="button" class="ghost small" (click)="test(p.name)"><app-icon name="refresh" /> Test</button>
                  <button type="button" class="ghost small" (click)="edit(p)"><app-icon name="edit" /> Edit</button>
                  <button type="button" class="icon danger-text" [attr.aria-label]="'Remove ' + p.name" (click)="remove(p.name)">
                    <app-icon name="delete" />
                  </button>
                </td>
              </tr>
            } @empty {
              <tr>
                <td colspan="5" class="muted">No peers. Add the worker whose directory you want to sync with.</td>
              </tr>
            }
          </tbody>
        </table>
      </div>

      @if (form(); as f) {
        <form class="form" (submit)="$event.preventDefault(); save()" novalidate>
          <h3>{{ editing() ? 'Edit ' + f.name : 'New peer' }}</h3>
          <div class="grid">
            <div class="field">
              <label for="p-name">Name</label>
              <input #pn id="p-name" [value]="f.name" [disabled]="editing()" maxlength="32" autocomplete="off" placeholder="east" (input)="setForm({ name: pn.value })" />
              @if (submitted() && !nameOk()) {
                <span class="field-error">Use 1-32 lower case letters, digits or _</span>
              } @else {
                <span class="hint">The name the peer has for itself (its worker name). Jobs are mirrored under it.</span>
              }
            </div>
            <div class="field">
              <label for="p-url">Sync server URL</label>
              <input #pu id="p-url" class="mono" [value]="f.url" autocomplete="off" placeholder="https://east:9081" (input)="setForm({ url: pu.value })" />
              @if (submitted() && !urlOk(f.url)) {
                <span class="field-error">Enter an http or https URL</span>
              }
            </div>
            <div class="field">
              <label for="p-user">User name</label>
              <input #pe id="p-user" [value]="f.username" autocomplete="off" (input)="setForm({ username: pe.value })" />
            </div>
            <div class="field">
              <label for="p-secret">Password</label>
              <select #ps id="p-secret" (change)="setForm({ passwordSecret: ps.value, passwordFile: ps.value ? '' : f.passwordFile })">
                <option value="" [selected]="!f.passwordSecret">None, use the password file</option>
                @for (secret of control.secrets(); track secret.name) {
                  <option [value]="secret.name" [selected]="secret.name === f.passwordSecret">{{ secret.name }}</option>
                }
              </select>
            </div>
            <div class="field">
              <label for="p-file">Password file</label>
              <input #pf id="p-file" class="mono" [value]="f.passwordFile" [disabled]="!!f.passwordSecret" autocomplete="off" (input)="setForm({ passwordFile: pf.value })" />
            </div>
            <div class="field">
              <label for="p-ca">CA file</label>
              <input #pc id="p-ca" class="mono" [value]="f.caFile" autocomplete="off" (input)="setForm({ caFile: pc.value })" />
              <span class="hint">Only for a self-signed certificate.</span>
            </div>
          </div>

          <h3>Control API of the peer</h3>
          <p class="muted">
            With the API, jobs are visible and manageable from both sides, a pair of directories is never synced twice at once, and the
            peer can start jobs. Without it only the directory is reached.
          </p>
          <div class="grid">
            <div class="field">
              <label for="a-url">URL</label>
              <input #au id="a-url" class="mono" [value]="f.apiUrl" autocomplete="off" placeholder="https://east:9080" (input)="setForm({ apiUrl: au.value })" />
              @if (submitted() && f.apiUrl && !urlOk(f.apiUrl)) {
                <span class="field-error">Enter an http or https URL</span>
              }
            </div>
            <div class="field">
              <label for="a-secret">Password</label>
              <select #asec id="a-secret" (change)="setForm({ apiPasswordSecret: asec.value, apiPasswordFile: asec.value ? '' : f.apiPasswordFile })">
                <option value="" [selected]="!f.apiPasswordSecret">None, use the password file</option>
                @for (secret of control.secrets(); track secret.name) {
                  <option [value]="secret.name" [selected]="secret.name === f.apiPasswordSecret">{{ secret.name }}</option>
                }
              </select>
              <span class="hint">The password of the portal of the peer.</span>
            </div>
            <div class="field">
              <label for="a-file">Password file</label>
              <input #af id="a-file" class="mono" [value]="f.apiPasswordFile" [disabled]="!!f.apiPasswordSecret" autocomplete="off" (input)="setForm({ apiPasswordFile: af.value })" />
            </div>
            <div class="field">
              <label for="a-ca">CA file</label>
              <input #ac id="a-ca" class="mono" [value]="f.apiCaFile" autocomplete="off" (input)="setForm({ apiCaFile: ac.value })" />
            </div>
          </div>

          <div class="form-actions">
            <button type="button" class="ghost" (click)="form.set(null)">Cancel</button>
            <button type="submit" class="primary" [disabled]="busy()">{{ busy() ? 'Saving…' : 'Save peer' }}</button>
          </div>
        </form>
      }
    </section>
  `,
})
export class PeersPage {
  protected readonly control = inject(ControlService);
  private readonly confirm = inject(ConfirmService);
  private readonly toast = inject(ToastService);

  protected readonly serve = signal<SyncServe | null>(null);
  protected readonly form = signal<Peer | null>(null);
  protected readonly editing = signal(false);
  protected readonly submitted = signal(false);
  protected readonly busy = signal(false);
  protected readonly error = signal('');
  protected readonly testing = signal('');
  protected readonly results = signal<Record<string, PeerStatus>>({});

  protected readonly nameOk = computed(() => NAME.test(this.form()?.name ?? ''));

  constructor() {
    void this.control.loadSecrets();
    void this.control.loadPeers();
    void this.control
      .loadSyncServe()
      .then((s) => this.serve.set(s))
      .catch((e) => this.error.set(message(e)));
  }

  protected setServe(change: Partial<SyncServe>): void {
    this.serve.update((s) => (s ? { ...s, ...change } : s));
  }

  protected async saveServe(): Promise<void> {
    const s = this.serve();
    if (!s) {
      return;
    }
    this.busy.set(true);
    this.error.set('');
    try {
      await this.control.saveSyncServe(s);
    } catch (e) {
      this.error.set(message(e));
    } finally {
      this.busy.set(false);
    }
  }

  protected urlOk(value: string): boolean {
    try {
      const u = new URL(value);
      return u.protocol === 'http:' || u.protocol === 'https:';
    } catch {
      return false;
    }
  }

  protected add(): void {
    this.editing.set(false);
    this.submitted.set(false);
    this.form.set(blankPeer());
  }

  protected edit(peer: Peer): void {
    this.editing.set(true);
    this.submitted.set(false);
    this.form.set({ ...peer });
  }

  protected setForm(change: Partial<Peer>): void {
    this.form.update((f) => (f ? { ...f, ...change } : f));
  }

  protected async save(): Promise<void> {
    const f = this.form();
    this.submitted.set(true);
    this.error.set('');
    if (!f || !this.nameOk() || !this.urlOk(f.url) || (f.apiUrl && !this.urlOk(f.apiUrl))) {
      return;
    }
    this.busy.set(true);
    try {
      await this.control.setPeer(f);
      this.form.set(null);
    } catch (e) {
      this.error.set(message(e));
    } finally {
      this.busy.set(false);
    }
  }

  protected async test(name: string): Promise<void> {
    this.testing.set(name);
    try {
      const r = await this.control.testPeer(name);
      this.results.update((all) => ({ ...all, [name]: r }));
    } catch (e) {
      this.toast.error(message(e));
    } finally {
      this.testing.set('');
    }
  }

  protected async remove(name: string): Promise<void> {
    const confirmed = await this.confirm.ask({
      title: `Remove ${name}?`,
      message: 'Sync jobs that use this peer have to be changed or deleted first.',
      action: 'Remove',
      danger: true,
    });
    if (!confirmed) {
      return;
    }
    try {
      await this.control.deletePeer(name);
    } catch (e) {
      this.error.set(message(e));
    }
  }
}

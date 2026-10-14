import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';

import { App } from './app';
import { ControlStatus, Defaults, Job, JobDefinition } from './global/model/backup/api/v1/control';
import { ListRunsResponse, ListSecretsResponse } from './global/model/backup/api/v1/control_service';
import { AuthService } from './global/services/auth/auth.service';
import { ControlService } from './global/services/control/control.service';
import { RpcService } from './global/services/rpc/rpc.service';
import { EMPTY } from 'rxjs';

describe('App on a fresh installation', () => {
  it('opens the setup instead of the overview', async () => {
    const rpc = {
      connected: signal(true),
      connections: signal(1),
      call: vi.fn(async (_s: string, method: string) => {
        switch (method) {
          case 'GetStatus':
            return ControlStatus.create({ defaults: Defaults.create({ passwordSecret: 'backup' }), jobs: [Job.create({ name: 'backup', type: 'backup', definition: JobDefinition.create({ name: 'backup', type: 'backup', enabled: true, schedule: '0 2 * * *' }) })] });
          case 'ListRuns':
            return ListRunsResponse.create();
          case 'ListSecrets':
            return ListSecretsResponse.create();
        }
        throw new Error('unexpected ' + method);
      }),
      stream: vi.fn(() => EMPTY),
    };
    TestBed.configureTestingModule({
      providers: [
        provideRouter([]),
        { provide: RpcService, useValue: rpc },
        { provide: AuthService, useValue: { ready: signal(true), signedIn: signal(true) } },
      ],
    });
    (globalThis as any).matchMedia = () => ({ matches: false });
    const fixture = TestBed.createComponent(App);
    TestBed.inject(ControlService).start();
    fixture.detectChanges();
    await fixture.whenStable();
    await new Promise((r) => setTimeout(r, 50));
    fixture.detectChanges();
    expect(fixture.nativeElement.textContent).toContain('Set up your backup');
  });
});

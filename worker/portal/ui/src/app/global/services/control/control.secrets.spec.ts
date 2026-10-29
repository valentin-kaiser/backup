import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';

import { SecretInfo } from '../../model/backup/api/v1/control';
import {
  ChangeRepositoryPasswordResponse,
  ListSecretsResponse,
  SetSecretResponse,
} from '../../model/backup/api/v1/control_service';
import { Empty } from '../../model/google/protobuf/empty';
import { RpcService } from '../rpc/rpc.service';
import { ControlService } from './control.service';

describe('ControlService secrets', () => {
  let control: ControlService;
  let calls: { method: string; params: any }[];
  let fail: string | null;

  beforeEach(() => {
    calls = [];
    fail = null;
    const rpc = {
      connected: signal(true),
      connections: signal(0),
      call: vi.fn(async (_service: string, method: string, params: any) => {
        calls.push({ method, params });
        if (fail) {
          throw new Error(fail);
        }
        switch (method) {
          case 'ListSecrets':
            return ListSecretsResponse.create({ secrets: [SecretInfo.create({ name: 'photos', repositories: ['main'] })] });
          case 'SetSecret':
            return SetSecretResponse.create({ secret: SecretInfo.create({ name: params.name }), generatedValue: params.generate ? 'generated-123' : '' });
          case 'ChangeRepositoryPassword':
            return ChangeRepositoryPasswordResponse.create({ repositories: 2, warnings: ['old key stays'], generatedValue: '' });
          default:
            return Empty.create();
        }
      }),
      stream: vi.fn(),
    };
    TestBed.configureTestingModule({ providers: [{ provide: RpcService, useValue: rpc }] });
    control = TestBed.inject(ControlService);
  });

  it('loads the secrets without values', async () => {
    await control.loadSecrets();
    expect(control.secrets().map((s) => s.name)).toEqual(['photos']);
    expect(JSON.stringify(control.secrets())).not.toMatch(/value/i);
  });

  it('returns a generated value only when one was asked for', async () => {
    expect(await control.setSecret('docs', 'my password', false)).toBe('');
    expect(calls[0]).toMatchObject({ method: 'SetSecret', params: { name: 'docs', value: 'my password', generate: false } });
    expect(await control.setSecret('docs', '', true)).toBe('generated-123');
    expect(calls.filter((c) => c.method === 'ListSecrets').length).toBe(2);
  });

  it('changes the repository password and reports what happened', async () => {
    const res = await control.changeRepositoryPassword('photos', 'new password', false);
    expect(res.repositories).toBe(2);
    expect(res.warnings).toEqual(['old key stays']);
    expect(calls[0]).toMatchObject({ method: 'ChangeRepositoryPassword', params: { name: 'photos', value: 'new password' } });
  });

  it('deletes a secret and reloads', async () => {
    await control.deleteSecret('photos');
    expect(calls.map((c) => c.method)).toEqual(['DeleteSecret', 'ListSecrets']);
  });

  it('passes the refusal of the daemon on', async () => {
    fail = 'the secret is still used by backup';
    await expect(control.deleteSecret('photos')).rejects.toThrow('still used');
  });
});

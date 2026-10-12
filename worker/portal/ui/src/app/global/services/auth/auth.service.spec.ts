import { TestBed } from '@angular/core/testing';

import { ControlService } from '../control/control.service';
import { RpcService } from '../rpc/rpc.service';
import { Session } from '../../model/backup/api/v1/auth';
import { ApiError, api } from '../../utils/api';
import { rpcCall, rpcFail, rpcResult } from '../../utils/api.testing';
import { AuthService } from './auth.service';

describe('AuthService', () => {
  let auth: AuthService;
  let fetchMock: ReturnType<typeof vi.fn>;
  let rpc: { connected: () => boolean; connect: ReturnType<typeof vi.fn>; disconnect: ReturnType<typeof vi.fn> };
  let control: { start: ReturnType<typeof vi.fn>; stop: ReturnType<typeof vi.fn>; reset: ReturnType<typeof vi.fn> };

  beforeEach(() => {
    fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    rpc = { connected: () => true, connect: vi.fn(), disconnect: vi.fn() };
    control = { start: vi.fn(), stop: vi.fn(), reset: vi.fn() };
    TestBed.configureTestingModule({
      providers: [
        { provide: RpcService, useValue: rpc },
        { provide: ControlService, useValue: control },
      ],
    });
    auth = TestBed.inject(AuthService);
  });

  afterEach(() => vi.unstubAllGlobals());

  it('stays signed out without a session and does not open the connection', async () => {
    fetchMock.mockResolvedValue(rpcFail(401, 'sign in required'));
    await auth.load();
    expect(auth.ready()).toBe(true);
    expect(auth.signedIn()).toBe(false);
    expect(rpc.connect).not.toHaveBeenCalled();
    expect(control.start).not.toHaveBeenCalled();
  });

  it('connects to the daemon once somebody is signed in and disconnects on sign-out', async () => {
    fetchMock.mockResolvedValueOnce(rpcResult({ signedIn: true }));
    await auth.load();
    expect(auth.signedIn()).toBe(true);
    expect(rpc.connect).toHaveBeenCalledTimes(1);
    expect(control.start).toHaveBeenCalledTimes(1);

    fetchMock.mockResolvedValueOnce(rpcResult({}));
    await auth.logout();
    expect(auth.signedIn()).toBe(false);
    expect(rpc.disconnect).toHaveBeenCalled();
    expect(control.stop).toHaveBeenCalled();
    expect(control.reset).toHaveBeenCalled();
  });

  it('sends only the password and reports a refusal', async () => {
    fetchMock.mockResolvedValueOnce(rpcFail(401, 'wrong password'));
    await expect(auth.login('wrong')).rejects.toThrow('wrong password');
    expect(auth.signedIn()).toBe(false);
    const call = rpcCall(fetchMock.mock.calls[0]);
    expect(call.method).toBe('Auth.Login');
    // Field 1 of the LoginRequest, nothing else is sent.
    expect(call.params).toEqual({ 1: 'wrong' });
  });

  it('signs in with the right password', async () => {
    fetchMock.mockResolvedValueOnce(rpcResult({ signedIn: true }));
    await auth.login('the password');
    expect(auth.signedIn()).toBe(true);
    expect(rpc.connect).toHaveBeenCalledTimes(1);
    expect(control.start).toHaveBeenCalledTimes(1);
  });

  it('has no notion of users or roles', () => {
    expect(Object.keys(auth)).not.toContain('user');
    expect((auth as any).isAdmin).toBeUndefined();
    expect((auth as any).canOperate).toBeUndefined();
  });

  it('keeps the page when only the network failed', async () => {
    fetchMock.mockResolvedValueOnce(rpcResult({ signedIn: true }));
    await auth.load();
    fetchMock.mockRejectedValue(new TypeError('network'));
    await (auth as any).verify();
    expect(auth.signedIn()).toBe(true);
    fetchMock.mockResolvedValue(rpcFail(401, 'sign in required'));
    await (auth as any).verify();
    expect(auth.signedIn()).toBe(false);
  });
});

describe('api', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('turns an unreachable portal into an error with status 0', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('network')));
    await expect(api('Auth', 'GetSession', {}, Session)).rejects.toMatchObject({ status: 0 });
  });

  it('keeps the status of an error response', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('cross-origin requests are not allowed', { status: 403 })));
    const error = await api('Auth', 'GetSession', {}, Session).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ status: 403, message: 'cross-origin requests are not allowed' });
  });
});

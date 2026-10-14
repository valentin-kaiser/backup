import { TestBed } from '@angular/core/testing';

import { ControlStatus, Event, EventType } from '../../model/backup/api/v1/control';
import { RpcService } from './rpc.service';

class FakeWebSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static instances: FakeWebSocket[] = [];

  readyState = FakeWebSocket.CONNECTING;
  sent: any[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: ((event: { code: number }) => void) | null = null;

  constructor(
    readonly url: string,
    readonly protocols: string[],
  ) {
    FakeWebSocket.instances.push(this);
  }

  send(data: string): void {
    this.sent.push(JSON.parse(data));
  }

  close(): void {
    this.readyState = 3;
  }

  open(): void {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
  }

  receive(frame: unknown): void {
    this.onmessage?.({ data: JSON.stringify(frame) });
  }

  serverClose(code: number): void {
    this.readyState = 3;
    this.onclose?.({ code });
  }
}

describe('RpcService', () => {
  let rpc: RpcService;
  const socket = () => FakeWebSocket.instances[FakeWebSocket.instances.length - 1];

  beforeEach(() => {
    FakeWebSocket.instances = [];
    vi.stubGlobal('WebSocket', FakeWebSocket);
    TestBed.configureTestingModule({});
    rpc = TestBed.inject(RpcService);
  });

  afterEach(() => {
    rpc.disconnect();
    vi.unstubAllGlobals();
  });

  it('opens the xrpc websocket of the portal', () => {
    rpc.connect();
    expect(socket().protocols).toEqual(['xrpc.json']);
    expect(socket().url).toMatch(/^wss?:\/\/.*\/api\/v1\/rpc$/);
  });

  it('correlates responses by id and decodes field numbers', async () => {
    rpc.connect();
    socket().open();

    const first = rpc.call('Control', 'GetStatus', {}, ControlStatus);
    const second = rpc.call('Control', 'GetStatus', {}, ControlStatus);
    await Promise.resolve();
    await Promise.resolve();
    const [a, b] = socket().sent;
    expect(a).toMatchObject({ jsonrpc: '2.0', method: 'Control.GetStatus', params: {} });
    expect(a.id).not.toBe(b.id);

    socket().receive({ jsonrpc: '2.0', id: b.id, result: { 1: 'v2', 2: true } });
    socket().receive({ jsonrpc: '2.0', id: a.id, result: { 1: 'v1' } });

    expect((await second).version).toBe('v2');
    expect((await first).version).toBe('v1');
  });

  it('rejects calls with the error of the server', async () => {
    rpc.connect();
    socket().open();
    const call = rpc.call('Control', 'GetRun', { id: 'x' }, ControlStatus);
    await Promise.resolve();
    await Promise.resolve();
    const request = socket().sent[0];
    expect(request.params).toEqual({ 1: 'x' });
    socket().receive({ jsonrpc: '2.0', id: request.id, error: { code: -32602, message: 'run not found' } });
    await expect(call).rejects.toMatchObject({ message: 'run not found', code: -32602 });
  });

  it('waits for the connection instead of failing calls made while connecting', async () => {
    rpc.connect();
    const call = rpc.call('Control', 'GetStatus', {}, ControlStatus);
    socket().open();
    await Promise.resolve();
    await Promise.resolve();
    expect(socket().sent).toHaveLength(1);
    socket().receive({ jsonrpc: '2.0', id: socket().sent[0].id, result: {} });
    await expect(call).resolves.toBeTruthy();
  });

  it('delivers stream messages and cancels on unsubscribe', async () => {
    rpc.connect();
    socket().open();
    const events: Event[] = [];
    const subscription = rpc.stream('Control', 'Watch', {}, Event).subscribe((e) => events.push(e));
    await Promise.resolve();
    await Promise.resolve();
    const request = socket().sent[0];
    expect(request.method).toBe('Control.Watch');

    socket().receive({
      jsonrpc: '2.0',
      method: 'xrpc.message',
      params: { id: request.id, data: { 1: 2, 3: 'backup', 5: 'hello' } },
    });
    expect(events).toHaveLength(1);
    expect(events[0]).toMatchObject({ type: EventType.EVENT_TYPE_LINE, job: 'backup', line: 'hello' });

    subscription.unsubscribe();
    expect(socket().sent[1]).toEqual({ jsonrpc: '2.0', method: 'xrpc.cancel', params: { id: request.id } });
  });

  it('errors streams and rejects calls when the connection is lost, then reconnects', async () => {
    vi.useFakeTimers();
    try {
      rpc.connect();
      socket().open();
      const errors: Error[] = [];
      rpc.stream('Control', 'Watch', {}, Event).subscribe({ error: (e) => errors.push(e) });
      const call = rpc.call('Control', 'GetStatus', {}, ControlStatus);
      call.catch(() => undefined);
      await vi.advanceTimersByTimeAsync(0);

      socket().serverClose(1006);
      await expect(call).rejects.toThrow('connection lost');
      expect(errors[0].message).toBe('connection lost');
      expect(rpc.connected()).toBe(false);

      await vi.advanceTimersByTimeAsync(15_000);
      expect(FakeWebSocket.instances).toHaveLength(2);
      socket().open();
      expect(rpc.connections()).toBe(2);
    } finally {
      vi.useRealTimers();
    }
  });
});

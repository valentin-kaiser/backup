import { Injectable, signal } from '@angular/core';
import { Observable } from 'rxjs';

import { fromWire, methodTypes, toWire } from '../../utils/xrpc-codec';

/** Sub-protocol of the xrpc websocket, see go-core/web/xrpc. */
const PROTOCOL = 'xrpc.json';

const CALL_TIMEOUT_MS = 150_000;
const OPEN_TIMEOUT_MS = 10_000;
const BACKOFF_MS = 1_000;
const MAX_BACKOFF_MS = 10_000;

export class RpcError extends Error {
  constructor(
    message: string,
    readonly code?: number,
  ) {
    super(message);
  }
}

interface Pending {
  resolve: (value: unknown) => void;
  reject: (error: Error) => void;
  decode: (value: unknown) => unknown;
  timer: ReturnType<typeof setTimeout>;
}

interface StreamEntry {
  next: (value: unknown) => void;
  error: (error: Error) => void;
  complete: () => void;
  decode: (value: unknown) => unknown;
}

interface Waiter {
  resolve: () => void;
  reject: (error: Error) => void;
  timer: ReturnType<typeof setTimeout>;
}

/** Message class of the generated models, only the JSON decoder is needed. */
export interface Decodable<T> {
  fromJSON(object: unknown): T;
}

/**
 * One websocket carries every call and every stream of the application. The protocol is xrpc
 * (JSON-RPC 2.0, protobuf field numbers on the wire): a call is a request with an id answered by a
 * response with the same id, a stream is a request answered by "xrpc.message" notifications until
 * the server ends it with the response.
 */
@Injectable({ providedIn: 'root' })
export class RpcService {
  /** Whether the websocket is open. */
  readonly connected = signal(false);
  /** Counts successful connects, consumers reload their state when it changes. */
  readonly connections = signal(0);

  private wanted = false;
  private socket: WebSocket | null = null;
  private retryTimer: ReturnType<typeof setTimeout> | undefined;
  private attempts = 0;
  private nextId = 1;
  private readonly pending = new Map<number, Pending>();
  private readonly streams = new Map<number, StreamEntry>();
  private waiters: Waiter[] = [];

  /** Opens the websocket to the control daemon and keeps it open, reconnecting with a backoff. */
  connect(): void {
    this.disconnect();
    this.wanted = true;
    this.open();
  }

  /** Closes the websocket and stops reconnecting. */
  disconnect(): void {
    this.wanted = false;
    clearTimeout(this.retryTimer);
    const socket = this.socket;
    this.socket = null;
    // Detach first so the close of the old socket cannot trigger a reconnect.
    if (socket) {
      socket.onclose = null;
      socket.close();
    }
    this.connected.set(false);
    this.failAll(new RpcError('disconnected'));
  }

  /** Calls a unary method, e.g. call('Control', 'GetStatus', {}, ControlStatus). */
  call<T>(service: string, method: string, params: object, response: Decodable<T>): Promise<T> {
    const types = methodTypes(service, method);
    return this.whenOpen().then(
      () =>
        new Promise<T>((resolve, reject) => {
          const id = this.nextId++;
          const timer = setTimeout(() => {
            this.pending.delete(id);
            reject(new RpcError(`${service}.${method} timed out`));
          }, CALL_TIMEOUT_MS);
          this.pending.set(id, {
            resolve: resolve as (value: unknown) => void,
            reject,
            decode: (value) => response.fromJSON(fromWire(types?.output, value ?? {})),
            timer,
          });
          this.send({
            jsonrpc: '2.0',
            id,
            method: `${service}.${method}`,
            params: toWire(types?.input, toJson(params)),
          });
        }),
    );
  }

  /**
   * Subscribes to a server stream. The observable errors when the connection is lost and completes
   * when the server ends the stream, callers resubscribe to continue.
   */
  stream<T>(service: string, method: string, params: object, message: Decodable<T>): Observable<T> {
    const types = methodTypes(service, method);
    return new Observable<T>((subscriber) => {
      const id = this.nextId++;
      let cancelled = false;
      this.whenOpen().then(
        () => {
          if (cancelled) {
            return;
          }
          this.streams.set(id, {
            next: (value) => subscriber.next(value as T),
            error: (error) => subscriber.error(error),
            complete: () => subscriber.complete(),
            decode: (value) => message.fromJSON(fromWire(types?.output, value ?? {})),
          });
          this.send({
            jsonrpc: '2.0',
            id,
            method: `${service}.${method}`,
            params: toWire(types?.input, toJson(params)),
          });
        },
        (error: Error) => subscriber.error(error),
      );
      return () => {
        cancelled = true;
        if (this.streams.delete(id)) {
          this.send({ jsonrpc: '2.0', method: 'xrpc.cancel', params: { id } });
        }
      };
    });
  }

  private url(): string {
    const url = new URL('api/v1/rpc', document.baseURI);
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
    return url.toString();
  }

  private open(): void {
    if (!this.wanted || this.socket) {
      return;
    }
    const socket = new WebSocket(this.url(), [PROTOCOL]);
    this.socket = socket;
    socket.onopen = () => {
      this.attempts = 0;
      this.connected.set(true);
      this.connections.update((n) => n + 1);
      this.flushWaiters();
    };
    socket.onmessage = (event: MessageEvent) => {
      try {
        this.handle(JSON.parse(event.data as string));
      } catch (error) {
        console.error('invalid message from the server', error);
      }
    };
    socket.onclose = () => {
      if (this.socket !== socket) {
        return;
      }
      this.socket = null;
      this.connected.set(false);
      this.failAll(new RpcError('connection lost'));
      this.scheduleReconnect();
    };
  }

  private scheduleReconnect(): void {
    if (!this.wanted) {
      return;
    }
    const delay = Math.min(MAX_BACKOFF_MS, BACKOFF_MS * 2 ** this.attempts++) + Math.random() * 300;
    this.retryTimer = setTimeout(() => this.open(), delay);
  }

  /** Resolves when the websocket is open, so calls issued while reconnecting are not lost. */
  private whenOpen(): Promise<void> {
    if (this.socket?.readyState === WebSocket.OPEN) {
      return Promise.resolve();
    }
    if (!this.wanted) {
      return Promise.reject(new RpcError('disconnected'));
    }
    return new Promise<void>((resolve, reject) => {
      const waiter: Waiter = {
        resolve,
        reject,
        timer: setTimeout(() => {
          this.waiters = this.waiters.filter((w) => w !== waiter);
          reject(new RpcError('not connected'));
        }, OPEN_TIMEOUT_MS),
      };
      this.waiters.push(waiter);
    });
  }

  private flushWaiters(): void {
    const waiters = this.waiters;
    this.waiters = [];
    for (const waiter of waiters) {
      clearTimeout(waiter.timer);
      waiter.resolve();
    }
  }

  private send(frame: unknown): void {
    if (this.socket?.readyState === WebSocket.OPEN) {
      this.socket.send(JSON.stringify(frame));
    }
  }

  private failAll(error: Error): void {
    const pending = [...this.pending.values()];
    const streams = [...this.streams.values()];
    const waiters = this.waiters;
    this.pending.clear();
    this.streams.clear();
    this.waiters = [];
    for (const p of pending) {
      clearTimeout(p.timer);
      p.reject(error);
    }
    for (const s of streams) {
      s.error(error);
    }
    for (const w of waiters) {
      clearTimeout(w.timer);
      w.reject(error);
    }
  }

  private handle(frame: any): void {
    if (frame?.method === 'xrpc.message') {
      const stream = this.streams.get(frame.params?.id);
      stream?.next(stream.decode(frame.params?.data));
      return;
    }
    const id = frame?.id;
    const pending = this.pending.get(id);
    if (pending) {
      this.pending.delete(id);
      clearTimeout(pending.timer);
      if (frame.error) {
        pending.reject(new RpcError(frame.error.message, frame.error.code));
      } else {
        try {
          pending.resolve(pending.decode(frame.result));
        } catch (error) {
          pending.reject(error as Error);
        }
      }
      return;
    }
    // The response of a stream request means the stream has ended.
    const stream = this.streams.get(id);
    if (stream) {
      this.streams.delete(id);
      if (frame.error) {
        stream.error(new RpcError(frame.error.message, frame.error.code));
      } else {
        stream.complete();
      }
    }
  }
}

/** Dates and bytes are JSON strings in protobuf JSON. */
function toJson(value: unknown): unknown {
  if (value instanceof Date) {
    return value.toISOString();
  }
  if (Array.isArray(value)) {
    return value.map(toJson);
  }
  if (value === null || typeof value !== 'object') {
    return value;
  }
  return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, toJson(item)]));
}

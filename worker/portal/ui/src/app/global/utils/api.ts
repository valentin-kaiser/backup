import type { Decodable } from '../services/rpc/rpc.service';
import { fromWire, methodTypes, toWire } from './xrpc-codec';

/** Path of the xrpc services of the portal itself (sign-in, controls, audit log). */
const ENDPOINT = 'api/v1/portal';

/** An error of the portal API. The status is the HTTP status, 0 when the portal could not be reached. */
export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
  }
}

let nextId = 1;

/** Dates and bytes are JSON strings in protobuf JSON. */
function toJson(value: unknown): unknown {
  if (value instanceof Date) {
    return value.toISOString();
  }
  if (value instanceof Uint8Array) {
    let binary = '';
    value.forEach((b) => (binary += String.fromCharCode(b)));
    return btoa(binary);
  }
  if (Array.isArray(value)) {
    return value.map(toJson);
  }
  if (value === null || typeof value !== 'object') {
    return value;
  }
  return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, toJson(item)]));
}

/**
 * Calls a method of the portal services, e.g. api('Auth', 'Login', { password }, Session). The session cookie travels
 * with it. The generated models encode the request and decode the answer.
 */
export async function api<T>(service: string, method: string, params: object, response: Decodable<T>): Promise<T> {
  const types = methodTypes(service, method);
  let reply: Response;
  try {
    reply = await fetch(new URL(ENDPOINT, document.baseURI), {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        jsonrpc: '2.0',
        id: nextId++,
        method: `${service}.${method}`,
        params: toWire(types?.input, toJson(params)),
      }),
    });
  } catch {
    throw new ApiError('The portal is not reachable', 0);
  }
  const text = await reply.text();
  let frame: { result?: unknown; error?: { message?: string } } | undefined;
  try {
    frame = text ? JSON.parse(text) : undefined;
  } catch {
    frame = undefined;
  }
  if (frame?.error || !reply.ok) {
    throw new ApiError(frame?.error?.message ?? (text.trim() || reply.statusText), reply.status);
  }
  return response.fromJSON(fromWire(types?.output, frame?.result ?? {}));
}

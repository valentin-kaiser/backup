/** Helpers for the specs of code that calls the portal services through `api`. */

/** The answer of a successful call. The result uses field names, the decoder accepts them. */
export function rpcResult(result: unknown = {}): Response {
  return new Response(JSON.stringify({ jsonrpc: '2.0', id: 1, result }), { status: 200 });
}

/** The answer of a failed call. */
export function rpcFail(status: number, message: string): Response {
  return new Response(JSON.stringify({ jsonrpc: '2.0', id: 1, error: { code: -32000, message } }), { status });
}

/** The call a `fetch` mock received: the method and the parameters with their field numbers. */
export function rpcCall(call: unknown[]): { method: string; params: Record<string, unknown> } {
  const init = call[1] as { body: string };
  const frame = JSON.parse(init.body);
  return { method: frame.method, params: compact(frame.params) as Record<string, unknown> };
}

/** Drops what protobuf leaves out on the wire anyway (empty strings, zeros, false) and empty messages. */
export function compact(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map(compact);
  }
  if (value && typeof value === 'object') {
    const out = Object.fromEntries(
      Object.entries(value)
        .map(([key, item]) => [key, compact(item)] as const)
        .filter(
          ([, item]) =>
            item !== '' && item !== 0 && item !== false && item !== undefined &&
            !(item && typeof item === 'object' && !Array.isArray(item) && Object.keys(item).length === 0),
        ),
    );
    return out;
  }
  return value;
}

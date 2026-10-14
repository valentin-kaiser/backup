import { FieldSchema, messages as generatedMessages, methods } from '../model/xrpc_schema';

type Schema = Record<string, Record<number, FieldSchema>>;

const REPEATED = 1;
const MAP = 2;

function isObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

/**
 * Translates messages between the name-keyed objects the generated models use and
 * the field-number-keyed JSON the gateway API speaks. Decoding also accepts name keys,
 * so it works whichever encoding the server answers with.
 */
export function createCodec(schema: Schema) {
  const numbersByName = new Map<string, Map<string, number>>();

  const numberOf = (type: string, name: string): number | undefined => {
    let index = numbersByName.get(type);
    if (!index) {
      index = new Map(
        Object.entries(schema[type] ?? {}).map(([num, field]) => [field[0], Number(num)]),
      );
      numbersByName.set(type, index);
    }
    return index.get(name);
  };

  const convertValue = (
    value: unknown,
    sub: string | undefined,
    repeat: number | undefined,
    convert: (type: string | undefined, value: unknown) => unknown,
  ): unknown => {
    if (!sub) return value;
    if (repeat === REPEATED && Array.isArray(value)) return value.map((item) => convert(sub, item));
    if (repeat === MAP && isObject(value)) {
      return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, convert(sub, item)]));
    }
    return convert(sub, value);
  };

  /** Name-keyed object to number-keyed wire form. Fields the schema does not know are kept as they are. */
  function toWire(type: string | undefined, value: unknown): unknown {
    const fields = type ? schema[type] : undefined;
    if (!fields || !isObject(value)) return value;

    const out: Record<string, unknown> = {};
    for (const [key, item] of Object.entries(value)) {
      if (item === undefined) continue;
      const num = numberOf(type!, key);
      if (num === undefined) {
        out[key] = item;
        continue;
      }
      const [, sub, repeat] = fields[num];
      out[num] = convertValue(item, sub, repeat, toWire);
    }
    return out;
  }

  /** Number-keyed (or name-keyed) wire form to a name-keyed object for the generated models' fromJSON. */
  function fromWire(type: string | undefined, value: unknown): unknown {
    const fields = type ? schema[type] : undefined;
    if (!fields || !isObject(value)) return value;

    const out: Record<string, unknown> = {};
    for (const [key, item] of Object.entries(value)) {
      const num = /^\d+$/.test(key) ? Number(key) : numberOf(type!, key);
      const field = num === undefined ? undefined : fields[num];
      if (!field) {
        out[key] = item;
        continue;
      }
      const [name, sub, repeat] = field;
      out[name] = convertValue(item, sub, repeat, fromWire);
    }
    return out;
  }

  return { toWire, fromWire };
}

export const { toWire, fromWire } = createCodec(generatedMessages);

/** Request and response message types of an RPC method, e.g. ('Gateways', 'List'). */
export function methodTypes(
  service: string,
  method: string,
): { input: string; output: string } | undefined {
  const entry = methods[`${service}.${method}`];
  return entry ? { input: entry[0], output: entry[1] } : undefined;
}

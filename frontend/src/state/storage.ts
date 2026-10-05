export type StorageCodec<T> = { encode(value: T): string; decode(raw: string): T };
export type BrowserStorage = "local" | "session";

const storage = (kind: BrowserStorage) => (kind === "local" ? window.localStorage : window.sessionStorage);

/** Browser preferences are optional; storage failures never change an operation's result. */
export function readStored<T>(kind: BrowserStorage, key: string, codec: StorageCodec<T>): T | undefined {
  try {
    const raw = storage(kind).getItem(key);
    return raw === null ? undefined : codec.decode(raw);
  } catch {
    return undefined;
  }
}

export function writeStored<T>(kind: BrowserStorage, key: string, value: T, codec: StorageCodec<T>): boolean {
  try {
    storage(kind).setItem(key, codec.encode(value));
    return true;
  } catch {
    return false;
  }
}

export function removeStored(kind: BrowserStorage, key: string): boolean {
  try {
    storage(kind).removeItem(key);
    return true;
  } catch {
    return false;
  }
}

export const stringCodec: StorageCodec<string> = { encode: (value) => value, decode: (raw) => raw };

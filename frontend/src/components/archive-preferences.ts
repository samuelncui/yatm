import { readStored, writeStored, type StorageCodec } from "@/state/storage";

export const archivePreferencesKey = "archive:last-options";
// An absent target Media count is off.
export type ArchivePreferences = { writeSignatureCache: boolean; targetMediaCount?: number };

const preferencesCodec: StorageCodec<ArchivePreferences> = {
  encode: (value) => JSON.stringify(value),
  decode: (raw) => {
    const value = JSON.parse(raw) as ArchivePreferences | null;
    if (!value || typeof value.writeSignatureCache !== "boolean") throw new Error("Invalid Archive preferences");
    if (value.targetMediaCount !== undefined && parseTargetMediaCount(String(value.targetMediaCount)) === undefined) {
      throw new Error("Invalid Archive preferences");
    }
    return value;
  },
};

export function loadArchivePreferences(): ArchivePreferences | undefined {
  return readStored("local", archivePreferencesKey, preferencesCodec);
}

export function saveArchivePreferences(value: ArchivePreferences) {
  writeStored("local", archivePreferencesKey, value, preferencesCodec);
}

/** Parses a target Media count input: a whole number of at least one, or undefined. */
export function parseTargetMediaCount(text: string): number | undefined {
  const value = text.trim();
  if (!/^[1-9]\d{0,9}$/.test(value) || Number(value) > 0xffffffff) return undefined;
  return Number(value);
}

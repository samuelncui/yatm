import { readStored, writeStored, type StorageCodec } from "@/state/storage";

export const archivePreferencesKey = "archive:last-options";
export type ArchivePreferences = { writeSignatureCache: boolean };

const preferencesCodec: StorageCodec<ArchivePreferences> = {
  encode: (value) => JSON.stringify(value),
  decode: (raw) => {
    const value = JSON.parse(raw) as ArchivePreferences | null;
    if (!value || typeof value.writeSignatureCache !== "boolean") throw new Error("Invalid Archive preferences");
    return value;
  },
};

export function loadArchivePreferences(): ArchivePreferences | undefined {
  return readStored("local", archivePreferencesKey, preferencesCodec);
}

export function saveArchivePreferences(value: ArchivePreferences) {
  writeStored("local", archivePreferencesKey, value, preferencesCodec);
}

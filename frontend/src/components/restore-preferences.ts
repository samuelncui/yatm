import { readStored, writeStored, type StorageCodec } from "@/state/storage";

export const restorePreferencesKey = "restore:last-options";
export type RestorePreferences = { writeSourceSignatureCache: boolean; writeTargetSignatureCache: boolean };

const preferencesCodec: StorageCodec<RestorePreferences> = {
  encode: (value) => JSON.stringify(value),
  decode: (raw) => {
    const value = JSON.parse(raw) as RestorePreferences | null;
    if (!value || typeof value.writeSourceSignatureCache !== "boolean" || typeof value.writeTargetSignatureCache !== "boolean") {
      throw new Error("Invalid Restore preferences");
    }
    return value;
  },
};

export function loadRestorePreferences(): RestorePreferences | undefined {
  return readStored("local", restorePreferencesKey, preferencesCodec);
}

export function saveRestorePreferences(value: RestorePreferences) {
  writeStored("local", restorePreferencesKey, value, preferencesCodec);
}

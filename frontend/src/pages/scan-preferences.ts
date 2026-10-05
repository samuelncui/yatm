import { MediaKind, PreviewPolicy, ScanResultPolicy, ScanSignaturePolicy } from "@/entity";
import { readStored, writeStored, type StorageCodec } from "@/state/storage";

export const scanPreferencesKey = "scan:last-options";
export type ScanPreferences = {
  kind: "location" | "media";
  location?: { id: string; rootPath: string };
  media?: { id: string; kind: MediaKind; identity: string };
  signaturePolicy: ScanSignaturePolicy;
  resultPolicy: ScanResultPolicy;
  compare: boolean;
  previewPolicy: PreviewPolicy;
};

const preferencesCodec: StorageCodec<ScanPreferences> = {
  encode: (value) => JSON.stringify(value),
  decode: (raw) => {
    const value = JSON.parse(raw) as ScanPreferences | null;
    if (!value || !["location", "media"].includes(value.kind) || typeof value.compare !== "boolean") throw new Error("Invalid Scan preferences");
    if (![ScanSignaturePolicy.FILL_MISSING, ScanSignaturePolicy.KNOWN_ONLY, ScanSignaturePolicy.FORCE_READ].includes(value.signaturePolicy))
      throw new Error("Invalid Scan signature policy");
    if (
      ![ScanResultPolicy.REPORT_ONLY, ScanResultPolicy.PUBLISH_ORIGINALS, ScanResultPolicy.PUBLISH_INVENTORY, ScanResultPolicy.VERIFY_COPIES].includes(
        value.resultPolicy,
      )
    )
      throw new Error("Invalid Scan result policy");
    if (![PreviewPolicy.NONE, PreviewPolicy.MISSING_ONLY, PreviewPolicy.REGENERATE_ALL].includes(value.previewPolicy))
      throw new Error("Invalid Scan Preview policy");
    if (value.location && (typeof value.location.id !== "string" || !/^[1-9]\d*$/.test(value.location.id) || typeof value.location.rootPath !== "string"))
      throw new Error("Invalid Scan Location preference");
    if (
      value.media &&
      (typeof value.media.id !== "string" ||
        !/^[1-9]\d*$/.test(value.media.id) ||
        typeof value.media.identity !== "string" ||
        ![MediaKind.TAPE, MediaKind.VOLUME].includes(value.media.kind))
    )
      throw new Error("Invalid Scan Media preference");
    return value;
  },
};

export function loadScanPreferences(): ScanPreferences | undefined {
  return readStored("local", scanPreferencesKey, preferencesCodec);
}

export function saveScanPreferences(value: ScanPreferences) {
  writeStored("local", scanPreferencesKey, value, preferencesCodec);
}

export function resultForSource(kind: string, result?: ScanResultPolicy) {
  if (result === ScanResultPolicy.REPORT_ONLY) return result;
  if (kind === "media") return result === ScanResultPolicy.VERIFY_COPIES ? result : ScanResultPolicy.PUBLISH_INVENTORY;
  return ScanResultPolicy.PUBLISH_ORIGINALS;
}

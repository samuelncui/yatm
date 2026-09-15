import { MediaKind, PreviewPolicy, ScanResultPolicy, ScanSignaturePolicy } from "@/entity";

export const scanPreferencesKey = "scan:last-options";
export type ScanPreferences = {
  kind: "location" | "media";
  location?: { id: string; rootPath: string; bindingToken: string };
  media?: { id: string; kind: MediaKind; identity: string };
  signaturePolicy: ScanSignaturePolicy;
  resultPolicy: ScanResultPolicy;
  compare: boolean;
  previewPolicy: PreviewPolicy;
};

export function loadScanPreferences(): ScanPreferences | undefined {
  try {
    const value = JSON.parse(localStorage.getItem(scanPreferencesKey) ?? "null") as ScanPreferences | null;
    if (!value || !["location", "media"].includes(value.kind) || typeof value.compare !== "boolean") return;
    if (![ScanSignaturePolicy.FILL_MISSING, ScanSignaturePolicy.KNOWN_ONLY, ScanSignaturePolicy.FORCE_READ].includes(value.signaturePolicy)) return;
    if (
      ![ScanResultPolicy.REPORT_ONLY, ScanResultPolicy.PUBLISH_ORIGINALS, ScanResultPolicy.PUBLISH_INVENTORY, ScanResultPolicy.VERIFY_COPIES].includes(
        value.resultPolicy,
      )
    )
      return;
    if (![PreviewPolicy.PREVIEW_NONE, PreviewPolicy.PREVIEW_MISSING_ONLY, PreviewPolicy.PREVIEW_REGENERATE_ALL].includes(value.previewPolicy)) return;
    if (
      value.location &&
      (typeof value.location.id !== "string" ||
        !/^[1-9]\d*$/.test(value.location.id) ||
        typeof value.location.rootPath !== "string" ||
        typeof value.location.bindingToken !== "string")
    )
      return;
    if (
      value.media &&
      (typeof value.media.id !== "string" ||
        !/^[1-9]\d*$/.test(value.media.id) ||
        typeof value.media.identity !== "string" ||
        ![MediaKind.TAPE, MediaKind.VOLUME].includes(value.media.kind))
    )
      return;
    return value;
  } catch {
    // Missing browser storage or obsolete preferences do not select a source.
  }
}

export function saveScanPreferences(value: ScanPreferences) {
  try {
    localStorage.setItem(scanPreferencesKey, JSON.stringify(value));
  } catch {
    // A browser preference must not turn successful Job creation into a failure.
  }
}

export function resultForSource(kind: string, result?: ScanResultPolicy) {
  if (result === ScanResultPolicy.REPORT_ONLY) return result;
  if (kind === "media") return result === ScanResultPolicy.VERIFY_COPIES ? result : ScanResultPolicy.PUBLISH_INVENTORY;
  return ScanResultPolicy.PUBLISH_ORIGINALS;
}

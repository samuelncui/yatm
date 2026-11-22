import { FileScope, PreviewPolicy, ScanResultPolicy, ScanSignaturePolicy, type ScanJobSpec } from "@/entity";

/** The source and options of the most recently created Scan, which a new Scan starts from. */
export type LastScan = {
  kind: "location" | "media";
  // Empty when that Scan read selected files, which a new Scan does not repeat.
  id: string;
  signaturePolicy: ScanSignaturePolicy;
  resultPolicy: ScanResultPolicy;
  previewPolicy: PreviewPolicy;
  writeSignatureCache: boolean;
};

export function lastScan(spec: ScanJobSpec): LastScan {
  const [selection] = spec.selections;
  const whole =
    spec.selections.length === 1 && selection.target.oneofKind === "location" && selection.target.location.path === "" && selection.scope === FileScope.ALL
      ? String(selection.target.location.locationId)
      : "";
  return {
    kind: spec.mediaId > 0n ? "media" : "location",
    id: spec.mediaId > 0n ? String(spec.mediaId) : whole,
    signaturePolicy: spec.signaturePolicy,
    resultPolicy: spec.resultPolicy,
    previewPolicy: spec.previewPolicy,
    // A Job created before this choice existed wrote the cache.
    writeSignatureCache: spec.writeSignatureCache ?? true,
  };
}

export function resultForSource(kind: string, result?: ScanResultPolicy) {
  if (result === ScanResultPolicy.REPORT_ONLY) return result;
  if (kind === "media") return result === ScanResultPolicy.VERIFY_COPIES ? result : ScanResultPolicy.PUBLISH_INVENTORY;
  return ScanResultPolicy.PUBLISH_ORIGINALS;
}

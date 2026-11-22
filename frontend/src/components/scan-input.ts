import { t } from "@lingui/core/macro";
import { FileSelection, PreviewPolicy, ScanResultPolicy, ScanSignaturePolicy } from "@/entity";
import type { ScanSelectionEntry } from "@/components/scan-selection";
import { resultForSource, type LastScan } from "./scan-preferences";

export type ScanOptions = {
  signaturePolicy: ScanSignaturePolicy;
  resultPolicy: ScanResultPolicy;
  previewPolicy: PreviewPolicy;
  writeSignatureCache: boolean;
};
export type ScanPrefill = {
  target?: { kind: "location" | "media"; id: string } | { kind: "files"; entries: ScanSelectionEntry[] };
  options?: Partial<ScanOptions>;
};
export const collectionOptions: Omit<ScanOptions, "writeSignatureCache"> = {
  signaturePolicy: ScanSignaturePolicy.KNOWN_ONLY,
  resultPolicy: ScanResultPolicy.PUBLISH_ORIGINALS,
  previewPolicy: PreviewPolicy.NONE,
};

function checkedPrefill(value: unknown): ScanPrefill {
  if (!value || typeof value !== "object") throw new Error(t`Invalid Scan shortcut. Choose the source again.`);
  const input = value as ScanPrefill;
  const target = input.target;
  if (target) {
    switch (target.kind) {
      case "location":
      case "media":
        if (typeof target.id !== "string" || !/^[1-9]\d*$/.test(target.id)) throw new Error(t`The selected source is invalid. Choose a source.`);
        break;
      case "files":
        if (
          !Array.isArray(target.entries) ||
          target.entries.length > 1000 ||
          target.entries.some(
            (entry) =>
              !entry ||
              !FileSelection.is(entry.selection) ||
              !entry.selection.target.oneofKind ||
              typeof entry.name !== "string" ||
              typeof entry.path !== "string" ||
              typeof entry.isDir !== "boolean",
          )
        )
          throw new Error(t`The selected files are invalid. Return to Files and select them again.`);
        break;
      default:
        throw new Error(t`Invalid Scan source. Choose a source.`);
    }
  }
  const options = input.options;
  if (
    options &&
    ((options.signaturePolicy !== undefined &&
      ![ScanSignaturePolicy.KNOWN_ONLY, ScanSignaturePolicy.FILL_MISSING, ScanSignaturePolicy.FORCE_READ].includes(options.signaturePolicy)) ||
      (options.resultPolicy !== undefined &&
        ![ScanResultPolicy.REPORT_ONLY, ScanResultPolicy.PUBLISH_ORIGINALS, ScanResultPolicy.PUBLISH_INVENTORY, ScanResultPolicy.VERIFY_COPIES].includes(
          options.resultPolicy,
        )) ||
      (options.previewPolicy !== undefined &&
        ![PreviewPolicy.NONE, PreviewPolicy.MISSING_ONLY, PreviewPolicy.REGENERATE_ALL].includes(options.previewPolicy)) ||
      (options.writeSignatureCache !== undefined && typeof options.writeSignatureCache !== "boolean"))
  )
    throw new Error(t`Invalid Scan options. Choose the source again.`);
  return input;
}

export function initialScan(params: URLSearchParams, state: unknown, saved?: LastScan) {
  let prefill: ScanPrefill = {};
  let error = "";
  try {
    const route = state as { scan?: unknown } | null;
    const location = params.get("location");
    const media = params.get("media");
    if (location && media) throw new Error(t`Choose one Scan source.`);
    if (media) prefill.target = { kind: "media", id: media };
    else if (location) prefill.target = { kind: "location", id: location };
    const options: Partial<ScanOptions> = {};
    if (params.get("result") === "verify") options.resultPolicy = ScanResultPolicy.VERIFY_COPIES;
    if (params.get("previews") === "1") options.previewPolicy = PreviewPolicy.MISSING_ONLY;
    if (params.get("collect") === "1") {
      Object.assign(options, collectionOptions);
      prefill.target ??= { kind: "files", entries: [] };
    }
    prefill.options = options;
    if (route?.scan !== undefined) {
      const shortcut = checkedPrefill(route.scan);
      prefill = { target: shortcut.target ?? prefill.target, options: { ...options, ...shortcut.options } };
    }
    prefill = checkedPrefill(prefill);
  } catch (failure) {
    error = failure instanceof Error ? failure.message : t`Invalid Scan shortcut.`;
    // Invalid explicit input must not fall back to a remembered whole source.
    prefill = { target: { kind: "files", entries: [] } };
  }
  const explicit = !!prefill.target;
  const target = prefill.target ?? (saved?.kind === "media" ? { kind: "media" as const, id: saved.id } : { kind: "location" as const, id: saved?.id ?? "" });
  const options: ScanOptions = {
    signaturePolicy: saved?.signaturePolicy ?? ScanSignaturePolicy.FILL_MISSING,
    previewPolicy: saved?.previewPolicy ?? PreviewPolicy.NONE,
    writeSignatureCache: saved?.writeSignatureCache ?? true,
    ...prefill.options,
    resultPolicy: resultForSource(target.kind, prefill.options?.resultPolicy ?? saved?.resultPolicy),
  };
  return { target, options, error, explicit };
}

export type InitialScan = ReturnType<typeof initialScan> & { priority?: bigint; creationReason?: string };

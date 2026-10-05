import { OriginalAvailability } from "@/entity";
import type { FileContentSummary } from "@/entity";
import { dateFromNs } from "@/tools/time";

export const contentTime = (value?: bigint, fallback = "Date unknown") =>
  dateFromNs(value)?.toLocaleString(undefined, { year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }) ?? fallback;

export const contentHex = (value: Uint8Array) => Array.from(value, (byte) => byte.toString(16).padStart(2, "0")).join("");

export type ArchiveTone = "success" | "warning" | "archive" | "error" | "info";
export type ArchiveMarker = { label: string; color: string; kind: "warning" | "changed" | "unknown" };
export type ArchiveSummary = { tone: ArchiveTone; title: string; description: string; marker?: ArchiveMarker };

// Availability and recorded restoration candidates determine the color, never check age.
export const archiveSummary = (state: FileContentSummary): ArchiveSummary => {
  const currentKnown = state.signatureKnown && state.currentObservationValid;
  const currentArchived = currentKnown && state.restorableCurrentCopyCount > 0n;
  const archived = currentArchived || state.restorableVersionCopyCount > 0n;
  const present = state.originalAvailability === OriginalAvailability.PRESENT;
  const absent = state.originalAvailability === OriginalAvailability.MISSING || state.originalAvailability === OriginalAvailability.UNLINKED;
  const knownArchive = archived || absent || currentKnown;
  const tone: ArchiveTone = !knownArchive || (!present && !absent) ? "info" : present ? (archived ? "success" : "warning") : archived ? "archive" : "error";
  const localLabels: Record<OriginalAvailability, string> = {
    [OriginalAvailability.UNSPECIFIED]: "Local file not checked",
    [OriginalAvailability.UNCHECKED]: "Local file not checked",
    [OriginalAvailability.PRESENT]: "Local file present",
    [OriginalAvailability.MISSING]: "Local file missing",
    [OriginalAvailability.UNLINKED]: "No local file linked",
    [OriginalAvailability.UNAVAILABLE]: "Local file unavailable",
  };
  const archive = archived
    ? currentArchived && present
      ? "Current content archived"
      : "Archive available"
    : knownArchive
      ? "No usable archive"
      : "Archive not checked";
  const details: string[] = [];
  const unavailableLatest = state.hasVersions && state.latestVersionId > 0n && state.latestVersionRestorableCopyCount === 0n;
  const anomalies = state.unhealthyVersionCopyCount > 0n || (state.currentObservationValid && state.unhealthyCopyCount > 0n);
  if (unavailableLatest) details.push(state.restorableVersionCopyCount > 0n ? "Latest version unavailable" : "Saved versions unavailable");
  if (anomalies) details.push(archived ? "Some copies need attention" : "Archive copies unavailable");
  if (present && currentKnown && !currentArchived && archived) details.push("Changes not archived");
  if (present && !currentKnown) details.push("Current content not checked");
  const marker: ArchiveMarker | undefined =
    unavailableLatest || anomalies
      ? { kind: "warning", color: archiveColors.warning, label: details[0] }
      : present && currentKnown && !currentArchived && archived
        ? { kind: "changed", color: archiveColors.warning, label: "Changes not archived" }
        : present && !currentKnown && archived
          ? { kind: "unknown", color: archiveColors.info, label: "Current content not checked" }
          : undefined;
  const title = [localLabels[state.originalAvailability], archive, marker?.label].filter(Boolean).join(" · ");
  return { tone, title, description: [localLabels[state.originalAvailability], archive, ...details].join(" · "), marker };
};

export const archiveColors = { success: "#15803d", warning: "#b77900", archive: "#2563eb", error: "#dc2626", info: "#87909e" } as const;

export const archiveIndicator = (summary: FileContentSummary) => {
  const status = archiveSummary(summary);
  return { color: archiveColors[status.tone], label: status.title, marker: status.marker };
};

import { OnlineBinding, OriginalAvailability } from "@/entity";
import type { FileContentSummary, Location } from "@/entity";

export const contentTime = (value?: bigint, fallback = "Date unknown") =>
  value ? new Date(Number(value)).toLocaleString(undefined, { year: "numeric", month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" }) : fallback;

export const contentHex = (value: Uint8Array) => Array.from(value, (byte) => byte.toString(16).padStart(2, "0")).join("");

export const locationSetupLabel = (location: Location) => {
  if (location.binding === OnlineBinding.UNCONFIRMED) return "Review imported path";
  return "Confirmed";
};

export type BackupTone = "success" | "warning" | "archive" | "error" | "info";
export type BackupMarker = { label: string; color: string; kind: "warning" | "changed" | "unknown" };
export type BackupSummary = { tone: BackupTone; title: string; description: string; marker?: BackupMarker };

// Availability and recorded restoration candidates determine the color, never check age.
export const backupSummary = (state: FileContentSummary): BackupSummary => {
  const currentKnown = state.signatureKnown && state.currentObservationValid;
  const currentBacked = currentKnown && state.restorableCurrentCopies > 0n;
  const backed = currentBacked || state.restorableVersionCopies > 0n;
  const present = state.originalAvailability === OriginalAvailability.ORIGINAL_PRESENT;
  const absent = state.originalAvailability === OriginalAvailability.ORIGINAL_MISSING || state.originalAvailability === OriginalAvailability.ORIGINAL_UNLINKED;
  const knownBackup = backed || absent || currentKnown;
  const tone: BackupTone = !knownBackup || (!present && !absent) ? "info" : present ? (backed ? "success" : "warning") : backed ? "archive" : "error";
  const localLabels: Record<OriginalAvailability, string> = {
    [OriginalAvailability.ORIGINAL_UNCHECKED]: "Local file not checked",
    [OriginalAvailability.ORIGINAL_PRESENT]: "Local file present",
    [OriginalAvailability.ORIGINAL_MISSING]: "Local file missing",
    [OriginalAvailability.ORIGINAL_UNLINKED]: "No local file linked",
    [OriginalAvailability.ORIGINAL_UNAVAILABLE]: "Local file unavailable",
  };
  const backup = backed
    ? currentBacked && present
      ? "Current content backed up"
      : "Backup available"
    : knownBackup
      ? "No usable backup"
      : "Backup not checked";
  const details: string[] = [];
  const unavailableLatest = state.hasVersions && state.latestVersionId > 0n && state.latestVersionRestorableCopies === 0n;
  const anomalies = state.unhealthyVersionCopies > 0n || (state.currentObservationValid && state.unhealthyCopies > 0n);
  if (unavailableLatest) details.push(state.restorableVersionCopies > 0n ? "Latest version unavailable" : "Saved versions unavailable");
  if (anomalies) details.push(backed ? "Some copies need attention" : "Backup copies unavailable");
  if (present && currentKnown && !currentBacked && backed) details.push("Changes not backed up");
  if (present && !currentKnown) details.push("Current content not checked");
  const marker: BackupMarker | undefined =
    unavailableLatest || anomalies
      ? { kind: "warning", color: backupColors.warning, label: details[0] }
      : present && currentKnown && !currentBacked && backed
        ? { kind: "changed", color: backupColors.warning, label: "Changes not backed up" }
        : present && !currentKnown && backed
          ? { kind: "unknown", color: backupColors.info, label: "Current content not checked" }
          : undefined;
  const title = [localLabels[state.originalAvailability], backup, marker?.label].filter(Boolean).join(" · ");
  return { tone, title, description: [localLabels[state.originalAvailability], backup, ...details].join(" · "), marker };
};

export const backupColors = { success: "#15803d", warning: "#b77900", archive: "#2563eb", error: "#dc2626", info: "#87909e" } as const;

export const backupIndicator = (summary: FileContentSummary) => {
  const status = backupSummary(summary);
  return { color: backupColors[status.tone], label: status.title, marker: status.marker };
};

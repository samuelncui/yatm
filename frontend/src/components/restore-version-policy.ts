import { FileScope, FileSelection, RestoreVersionMatch, type RestoreVersionResolution } from "@/entity";
import { contentTime } from "@/components/content-status";
import type { SelectionEntry } from "@/components/selection-waitlist";

export type RestorePolicy = { mode: "latest" | "before"; date: string };

export const restoreCutoff = (policy: RestorePolicy): bigint | undefined => {
  if (policy.mode !== "before" || !policy.date) return undefined;
  const value = new Date(policy.date).getTime();
  return Number.isFinite(value) && value >= 0 ? BigInt(value) : undefined;
};

export const followRestorePolicy = (entries: SelectionEntry[]): SelectionEntry[] => {
  const result = new Map<string, SelectionEntry>();
  for (const entry of entries) {
    if (!entry.version) {
      result.set(entry.key, entry);
      continue;
    }
    const selection = FileSelection.create({ target: { oneofKind: "library", library: { fileId: entry.version.fileId } }, scope: FileScope.SAVED });
    const next = { ...entry, key: FileSelection.toJsonString(selection), fileID: String(entry.version.fileId), selection, version: undefined };
    result.set(next.key, next);
  }
  return [...result.values()];
};

export const restoreVersionLabel = (entry: SelectionEntry, resolution?: RestoreVersionResolution, cutoff?: bigint): string => {
  if (entry.isDir) return cutoff !== undefined ? `Latest backups at or before ${contentTime(cutoff)}` : "Latest saved versions";
  if (entry.version) {
    const time = entry.version.lastArchivedAtMs ?? entry.version.firstArchivedAtMs;
    const outside = cutoff !== undefined && entry.version.firstArchivedAtMs !== undefined && entry.version.firstArchivedAtMs > cutoff;
    return `Custom version · ${contentTime(time)}${outside ? " · After selected time" : ""}`;
  }
  if (!resolution) return "Finding version…";
  switch (resolution.match) {
    case RestoreVersionMatch.RESTORE_VERSION_NO_SAVED_VERSION:
      return "No saved version";
    case RestoreVersionMatch.RESTORE_VERSION_AFTER_CUTOFF:
      return "No backup at or before selected time";
    case RestoreVersionMatch.RESTORE_VERSION_DATE_UNKNOWN:
      return "Backup date unknown";
    default:
      return `Saved ${contentTime(resolution.archivedAtMs)}${cutoff !== undefined ? " · Follows selected time" : " · Latest"}`;
  }
};

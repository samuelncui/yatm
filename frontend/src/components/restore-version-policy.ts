import { FileScope, FileSelection, RestoreVersionMatch, type RestoreVersionResolution } from "@/entity";
import { contentTime } from "@/components/content-status";
import type { SelectionEntry } from "@/components/selection-waitlist";
import { dateToNs, parseUnixNs } from "@/tools/time";

export type RestorePolicy = { mode: "latest" | "before"; date: string; cutoff?: { valueNs: string; date: string } };

export const restoreCutoff = (policy: RestorePolicy): bigint | undefined => {
  if (policy.mode !== "before") return undefined;
  if (policy.cutoff?.date === policy.date) return parseUnixNs(policy.cutoff.valueNs);
  if (!policy.date) return undefined;
  return dateToNs(new Date(policy.date));
};

export const followRestorePolicy = (entries: SelectionEntry[]): SelectionEntry[] => {
  const result = new Map<string, SelectionEntry>();
  for (const entry of entries) {
    if (!entry.version || entry.unavailableReason) {
      result.set(entry.key, entry);
      continue;
    }
    const selection = FileSelection.create({ target: { oneofKind: "library", library: { fileId: entry.version.fileId } }, scope: FileScope.SAVED });
    const next = {
      ...entry,
      key: FileSelection.toJsonString(selection),
      fileID: String(entry.version.fileId),
      selection,
      version: undefined,
      versionID: undefined,
    };
    result.set(next.key, next);
  }
  return [...result.values()];
};

export const restoreVersionLabel = (entry: SelectionEntry, resolution?: RestoreVersionResolution, cutoff?: bigint): string => {
  if (entry.isDir) return cutoff !== undefined ? `Latest archives at or before ${contentTime(cutoff)}` : "Latest saved versions";
  if (entry.version) {
    const time = entry.version.lastArchivedAtNs ?? entry.version.firstArchivedAtNs;
    const outside = cutoff !== undefined && entry.version.firstArchivedAtNs !== undefined && entry.version.firstArchivedAtNs > cutoff;
    return `Custom version · ${contentTime(time)}${outside ? " · After selected time" : ""}`;
  }
  if (!resolution) return "Finding version…";
  switch (resolution.match) {
    case RestoreVersionMatch.NO_SAVED_VERSION:
      return "No saved version";
    case RestoreVersionMatch.AFTER_CUTOFF:
      return "No archive at or before selected time";
    case RestoreVersionMatch.DATE_UNKNOWN:
      return "Archive date unknown";
    default:
      return `Saved ${contentTime(resolution.archivedAtNs)}${cutoff !== undefined ? " · Follows selected time" : " · Latest"}`;
  }
};

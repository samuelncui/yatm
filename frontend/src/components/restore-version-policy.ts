import { t } from "@lingui/core/macro";
import { FileScope, FileSelection, RestoreVersionMatch, type RestoreVersionResolution } from "@/entity";
import { contentTime, versionArchiveLabel } from "@/components/content-status";
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
  if (entry.isDir) {
    if (cutoff === undefined) return t`Latest saved versions`;
    const time = contentTime(cutoff);
    return t`Latest archives at or before ${time}`;
  }
  if (entry.version) {
    const outside = cutoff !== undefined && entry.version.firstArchivedAtNs !== undefined && entry.version.firstArchivedAtNs > cutoff;
    const date = versionArchiveLabel(entry.version);
    return outside ? t`Custom version · ${date} · After selected time` : t`Custom version · ${date}`;
  }
  if (!resolution) return t`Finding version…`;
  switch (resolution.match) {
    case RestoreVersionMatch.NO_SAVED_VERSION:
      return t`No saved version`;
    case RestoreVersionMatch.AFTER_CUTOFF:
      return t`No archive at or before selected time`;
    case RestoreVersionMatch.DATE_UNKNOWN:
      return t`Archive date unknown`;
    default: {
      const time = contentTime(resolution.archivedAtNs);
      return cutoff !== undefined ? t`Saved ${time} · Follows selected time` : t`Saved ${time} · Latest`;
    }
  }
};

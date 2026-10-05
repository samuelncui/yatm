import { createSelector, createSlice, type PayloadAction } from "@reduxjs/toolkit";
import { FileSelection, FileVersion } from "@/entity";
import type { RestorePolicy } from "@/components/restore-version-policy";
import { parseUnixNs } from "@/tools/time";
import { readStored, writeStored, type StorageCodec } from "./storage";
import type { AppStore } from "./store";

export type SelectionKind = "archive" | "restore";

export type SelectionEntry = {
  key: string;
  name: string;
  path: string;
  isDir?: boolean;
  selection?: FileSelection;
  version?: FileVersion;
  versionID?: string;
  unavailableReason?: string;
  fileID?: string;
  target?: string;
  size?: number;
};

export type SelectionMergeResult = {
  entries: SelectionEntry[];
  added: number;
  duplicates: number;
  replacedAutomatic: number;
  keptExplicit: number;
};

export const selectionStorageKey = (kind: SelectionKind) => `job-selection:${kind}`;

export type StoredSelectionEntry = Omit<SelectionEntry, "selection" | "version"> & { selection?: string; version?: string };
export const encodeSelection = (entry: SelectionEntry): StoredSelectionEntry => ({
  ...entry,
  selection: entry.selection ? FileSelection.toJsonString(entry.selection) : undefined,
  version: entry.version ? FileVersion.toJsonString(entry.version) : undefined,
});
export const decodeSelection = (entry: StoredSelectionEntry): SelectionEntry => ({
  ...entry,
  selection: entry.selection ? FileSelection.fromJsonString(entry.selection) : undefined,
  version: entry.version ? FileVersion.fromJsonString(entry.version) : undefined,
});

export const selectionCodec: StorageCodec<SelectionEntry[]> = {
  encode: (entries) => JSON.stringify(entries.map(encodeSelection)),
  decode: (raw) => {
    const saved = JSON.parse(raw) as StoredSelectionEntry[];
    if (!Array.isArray(saved)) throw new Error("Invalid selection list");
    return saved.flatMap((stored) => {
      const entry = decodeSelection(stored);
      if (!entry.selection && !entry.version && !entry.versionID) return [];
      return [
        {
          ...entry,
          key: entry.version || entry.versionID ? `version:${entry.version?.id ?? entry.versionID}` : FileSelection.toJsonString(entry.selection!),
          name: typeof entry.name === "string" ? entry.name : "",
          path: typeof entry.path === "string" ? entry.path : typeof entry.name === "string" ? entry.name : "",
        },
      ];
    });
  },
};

export const loadSelectionEntries = (kind: SelectionKind) => readStored("session", selectionStorageKey(kind), selectionCodec) ?? [];
export const saveSelectionEntries = (kind: SelectionKind, entries: SelectionEntry[]) =>
  writeStored("session", selectionStorageKey(kind), entries, selectionCodec);
export const restorePolicyKey = `${selectionStorageKey("restore")}:version-policy`;
export const restorePolicyCodec: StorageCodec<RestorePolicy> = {
  encode: JSON.stringify,
  decode: (raw) => {
    const value = JSON.parse(raw);
    if ((value?.mode !== "before" && value?.mode !== "latest") || typeof value.date !== "string") throw new Error("Invalid Restore policy");
    const cutoff = value.cutoff;
    if (cutoff !== undefined && (!cutoff || typeof cutoff.valueNs !== "string" || parseUnixNs(cutoff.valueNs) === undefined || typeof cutoff.date !== "string"))
      throw new Error("Invalid Restore cutoff");
    return { mode: value.mode, date: value.date, ...(cutoff ? { cutoff } : {}) };
  },
};

export function mergeSelectionEntries<
  T extends Pick<SelectionEntry, "key" | "isDir" | "fileID" | "unavailableReason"> & { selection?: unknown; version?: unknown },
>(current: T[], additions: T[]) {
  const entries = new Map(current.map((entry) => [entry.key, entry]));
  const byFile = new Map<string | undefined, { automatic: Set<string>; explicit: Set<string> }>();
  const index = (entry: T) => {
    if (!entry.version && (!entry.selection || entry.isDir)) return;
    const fileID = entry.fileID;
    let group = byFile.get(fileID);
    if (!group) {
      group = { automatic: new Set(), explicit: new Set() };
      byFile.set(fileID, group);
    }
    if (entry.selection && !entry.isDir) group.automatic.add(entry.key);
    if (entry.version) group.explicit.add(entry.key);
  };
  for (const entry of entries.values()) index(entry);
  let added = 0;
  let duplicates = 0;
  let replacedAutomatic = 0;
  let keptExplicit = 0;

  for (const addition of additions) {
    const fileID = addition.fileID;
    const group = byFile.get(fileID);
    if (addition.selection && !addition.isDir && fileID && group?.explicit.size) {
      keptExplicit++;
      continue;
    }
    if (addition.version && group) {
      for (const key of group.automatic) {
        entries.delete(key);
        group.explicit.delete(key);
        replacedAutomatic++;
      }
      group.automatic.clear();
    }
    const existing = entries.get(addition.key);
    if (existing) {
      if (existing.unavailableReason && !addition.unavailableReason) {
        entries.set(addition.key, addition);
        index(addition);
        added++;
      } else duplicates++;
      continue;
    }
    entries.set(addition.key, addition);
    index(addition);
    added++;
  }

  return {
    entries: added || replacedAutomatic ? [...entries.values()] : current,
    added,
    duplicates,
    replacedAutomatic,
    keptExplicit,
  };
}

type SelectionState = { entries: StoredSelectionEntry[]; revision: number };
export type SelectionsState = Record<SelectionKind, SelectionState> & { versionPolicy: RestorePolicy };
export const initialSelections = (): SelectionsState => ({
  archive: { entries: loadSelectionEntries("archive").map(encodeSelection), revision: 0 },
  restore: { entries: loadSelectionEntries("restore").map(encodeSelection), revision: 0 },
  versionPolicy: readStored("session", restorePolicyKey, restorePolicyCodec) ?? { mode: "latest", date: "" },
});
const selections = createSlice({
  name: "selections",
  initialState: {
    archive: { entries: [], revision: 0 },
    restore: { entries: [], revision: 0 },
    versionPolicy: { mode: "latest", date: "" },
  } as SelectionsState,
  reducers: {
    creationLoaded(state, { payload }: PayloadAction<{ kind: SelectionKind; revision: number; entries: StoredSelectionEntry[]; policy?: RestorePolicy }>) {
      const current = state[payload.kind];
      if (current.revision !== payload.revision) return;
      current.entries = payload.entries;
      current.revision++;
      if (payload.kind === "restore" && payload.policy) state.versionPolicy = payload.policy;
    },
    replaced(state, { payload }: PayloadAction<{ kind: SelectionKind; entries: StoredSelectionEntry[] }>) {
      state[payload.kind].entries = payload.entries;
      state[payload.kind].revision++;
    },
    submitted(state, { payload }: PayloadAction<{ kind: SelectionKind; revision: number }>) {
      const current = state[payload.kind];
      if (current.revision !== payload.revision) return;
      current.entries = [];
      current.revision++;
    },
    policyChanged(state, { payload }: PayloadAction<RestorePolicy>) {
      state.versionPolicy = payload;
      state.restore.revision++;
    },
  },
});
export const selectionActions = selections.actions;
export const selectionsReducer = selections.reducer;

/** Compute the merge once, then publish its rows and return the matching operation counts. */
export function addSelectionEntries(store: AppStore, kind: SelectionKind, additions: SelectionEntry[]) {
  const current = store.getState().selections[kind].entries;
  const { entries, ...counts } = mergeSelectionEntries(current, additions.map(encodeSelection));
  if (entries !== current) store.dispatch(selectionActions.replaced({ kind, entries }));
  return counts;
}

export const makeSelectEntries = (kind: SelectionKind) =>
  createSelector([(state: { selections: SelectionsState }) => state.selections[kind].entries], (entries) => entries.map(decodeSelection));

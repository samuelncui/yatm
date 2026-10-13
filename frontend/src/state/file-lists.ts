import { createSlice, type PayloadAction } from "@reduxjs/toolkit";
import { readStored, type StorageCodec } from "./storage";

export type FileListPreferences = { sort: "name" | "size" | "date"; descending: boolean; foldersFirst: boolean };
export type FileListState = Record<string, FileListPreferences>;
export const defaultFileListPreferences: FileListPreferences = { sort: "name", descending: false, foldersFirst: true };
export const fileListStorageKey = "file-browser:preferences";
export const fileListCodec: StorageCodec<FileListState> = {
  encode: JSON.stringify,
  decode: (raw) => {
    const saved = JSON.parse(raw);
    if (!saved || typeof saved !== "object" || Array.isArray(saved)) throw new Error("Invalid file list preferences");
    return Object.fromEntries(
      Object.entries(saved).flatMap(([id, value]) => {
        const item = value as FileListPreferences | null;
        if (!item || !["name", "size", "date"].includes(item.sort) || typeof item.descending !== "boolean" || typeof item.foldersFirst !== "boolean") return [];
        return [[id, { sort: item.sort, descending: item.descending, foldersFirst: item.foldersFirst }]];
      }),
    );
  },
};
export const initialFileLists = () => readStored("local", fileListStorageKey, fileListCodec) ?? {};
const slice = createSlice({
  name: "fileLists",
  initialState: {} as FileListState,
  reducers: {
    changed: (state, { payload }: PayloadAction<{ id: string; preferences: FileListPreferences }>) => {
      state[payload.id] = payload.preferences;
    },
  },
});
export const fileListActions = slice.actions;
export const fileListsReducer = slice.reducer;

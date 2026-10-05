import { createSelector, createSlice, type PayloadAction } from "@reduxjs/toolkit";
import { SettingsGroup, SettingsValue } from "@/entity";

export type SettingsEntry = { value?: string; request: number; loading: boolean; error: string };
export type SettingsState = Partial<Record<SettingsGroup, SettingsEntry>>;
const settings = createSlice({
  name: "settings",
  initialState: {} as SettingsState,
  reducers: {
    requested(state, { payload: group }: PayloadAction<SettingsGroup>) {
      const previous = state[group];
      state[group] = { ...previous, request: (previous?.request ?? 0) + 1, loading: true, error: "" };
    },
    received(state, { payload }: PayloadAction<{ group: SettingsGroup; request: number; value: string }>) {
      const current = state[payload.group];
      if (!current || current.request !== payload.request) return;
      current.value = payload.value;
      current.loading = false;
      current.error = "";
    },
    failed(state, { payload }: PayloadAction<{ group: SettingsGroup; request: number; error: string }>) {
      const current = state[payload.group];
      if (!current || current.request !== payload.request) return;
      current.loading = false;
      current.error = payload.error;
    },
    saved(state, { payload }: PayloadAction<{ group: SettingsGroup; value: string }>) {
      state[payload.group] = { value: payload.value, request: (state[payload.group]?.request ?? 0) + 1, loading: false, error: "" };
    },
  },
});
export const settingsActions = settings.actions;
export const settingsReducer = settings.reducer;
export const makeSelectSettings = (group: SettingsGroup) =>
  createSelector([(state: { settings: SettingsState }) => state.settings[group]?.value], (value) =>
    value === undefined ? undefined : SettingsValue.fromJsonString(value),
  );

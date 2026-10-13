import { createSelector, createSlice, type PayloadAction } from "@reduxjs/toolkit";
import { locationCli } from "@/api";
import { Location } from "@/entity";
import { errorMessage } from "@/tools";
import type { AppStore } from "./store";

type LocationsState = {
  entries: Record<string, string>;
  loaded: boolean;
  loading: boolean;
  error: string;
  request: number;
};
const initialState: LocationsState = { entries: {}, loaded: false, loading: false, error: "", request: 0 };
const locations = createSlice({
  name: "locations",
  initialState,
  reducers: {
    requested(state) {
      state.request++;
      state.loading = true;
      state.error = "";
    },
    received(state, { payload }: PayloadAction<{ request: number; entries: Record<string, string> }>) {
      if (state.request !== payload.request) return;
      state.entries = payload.entries;
      state.loaded = true;
      state.loading = false;
    },
    failed(state, { payload }: PayloadAction<{ request: number; error: string }>) {
      if (state.request !== payload.request) return;
      state.loading = false;
      state.error = payload.error;
    },
    saved(state, { payload }: PayloadAction<{ id: string; value: string }>) {
      state.entries[payload.id] = payload.value;
      state.request++;
      state.loading = false;
      state.error = "";
    },
    removed(state, { payload: id }: PayloadAction<string>) {
      delete state.entries[id];
      state.request++;
      state.loading = false;
      state.error = "";
    },
  },
});
const locationActions = locations.actions;
export const locationsReducer = locations.reducer;
export const selectLocations = createSelector([(state: { locations: LocationsState }) => state.locations.entries], (entries) =>
  Object.values(entries)
    .map((value) => Location.fromJsonString(value))
    .sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0)),
);

/** One complete configured catalog per application; refresh retains the last complete list. */
export async function loadLocations(store: AppStore, refresh = false) {
  const current = store.getState().locations;
  if (!refresh && (current.loading || current.loaded || current.error)) return;
  // A refresh after import must supersede even a pending pre-import read.
  store.dispatch(locationActions.requested());
  const request = store.getState().locations.request;
  const entries: Record<string, string> = {};
  let afterId = 0n;
  try {
    for (;;) {
      const page = await locationCli.list({ afterId, limit: 50, query: "" }).response;
      if (store.getState().locations.request !== request) return;
      for (const location of page.locations) entries[String(location.id)] = Location.toJsonString(location);
      if (!page.hasMore) break;
      const next = page.locations.at(-1)?.id;
      if (next === undefined || next <= afterId) throw new Error("Could not load the complete Location catalog");
      afterId = next;
    }
    store.dispatch(locationActions.received({ request, entries }));
  } catch (error) {
    store.dispatch(locationActions.failed({ request, error: errorMessage(error, "Could not load Locations") }));
  }
}

export function saveLocation(store: AppStore, location: Location) {
  store.dispatch(locationActions.saved({ id: String(location.id), value: Location.toJsonString(location) }));
  void loadLocations(store);
}

export function removeLocation(store: AppStore, id: bigint) {
  store.dispatch(locationActions.removed(String(id)));
  void loadLocations(store);
}

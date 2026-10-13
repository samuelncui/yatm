import { act, renderHook, waitFor } from "@testing-library/react";
import { StrictMode, type PropsWithChildren } from "react";
import { beforeEach, expect, it, vi } from "vitest";
import { Location } from "@/entity";
import { AppStateProvider } from "@/state/react";
import { createAppStore } from "@/state/store";
import { loadLocations, removeLocation, saveLocation, selectLocations } from "@/state/locations";
import { useLocationChoices } from "./use-location-choices";

const { list } = vi.hoisted(() => ({ list: vi.fn() }));
vi.mock("@/api", () => ({ locationCli: { list } }));
const first = Location.create({ id: 1n, name: "First", rootPath: "/photos", restoreTarget: true });
const second = Location.create({ id: 2n, name: "Second", rootPath: "/other" });
const call = (locations: Location[], hasMore = false) => ({ response: Promise.resolve({ locations, hasMore }) });
const pendingPage = () => {
  let resolve!: (page: { locations: Location[]; hasMore: boolean }) => void;
  let reject!: (error: Error) => void;
  const response = new Promise<{ locations: Location[]; hasMore: boolean }>((complete, fail) => {
    resolve = complete;
    reject = fail;
  });
  return { response, resolve, reject };
};
const fixture = () => {
  const store = createAppStore();
  const wrapper = ({ children }: PropsWithChildren) => <AppStateProvider store={store}>{children}</AppStateProvider>;
  return { store, wrapper };
};
beforeEach(() => list.mockReset().mockReturnValue(call([])));

it("reuses the proactively loaded complete catalog across opening, closing and simultaneous choosers", async () => {
  const rows = Array.from({ length: 51 }, (_, index) => Location.create({ id: BigInt(index + 1), name: `Location ${index + 1}` }));
  list.mockReturnValueOnce(call(rows.slice(0, 50), true)).mockReturnValueOnce(call(rows.slice(50)));
  const { store, wrapper } = fixture();
  await loadLocations(store);
  const one = renderHook(({ enabled }) => useLocationChoices({ enabled }), { wrapper, initialProps: { enabled: false } });
  expect(one.result.current.locations).toEqual(rows);
  one.rerender({ enabled: true });
  expect(one.result.current.loading).toBe(false);
  expect(one.result.current.locations).toHaveLength(51);
  one.rerender({ enabled: false });
  one.rerender({ enabled: true });
  const two = renderHook(() => useLocationChoices({ enabled: true }), { wrapper });
  expect(two.result.current.locations).toEqual(rows);
  expect(list.mock.calls.map(([request]) => request)).toEqual([
    { afterId: 0n, limit: 50, query: "" },
    { afterId: 50n, limit: 50, query: "" },
  ]);
});

it("shares initial loading across StrictMode and consumers without cancellation on close or unmount", async () => {
  const pending = pendingPage();
  list.mockReturnValueOnce(pending);
  const { store, wrapper } = fixture();
  const one = renderHook(({ enabled }) => useLocationChoices({ enabled }), {
    wrapper: ({ children }) => <StrictMode>{wrapper({ children })}</StrictMode>,
    initialProps: { enabled: true },
  });
  const two = renderHook(() => useLocationChoices({ enabled: true }), { wrapper });
  expect(one.result.current.loading).toBe(true);
  one.rerender({ enabled: false });
  one.unmount();
  expect(list).toHaveBeenCalledOnce();
  await act(async () => pending.resolve({ locations: [first], hasMore: false }));
  expect(two.result.current.locations).toEqual([first]);
  expect(selectLocations(store.getState())).toEqual([first]);
});

it("filters names, paths and both Restore preferences locally without replacing the catalog", async () => {
  const literal = Location.create({ id: 3n, name: "100%_archive", rootPath: "/CaseSensitive" });
  list.mockReturnValueOnce(call([first, second, literal]));
  const { wrapper } = fixture();
  const { result, rerender } = renderHook(
    ({ query, restoreTarget }: { query: string; restoreTarget?: boolean }) => useLocationChoices({ enabled: true, query, restoreTarget }),
    { wrapper, initialProps: { query: "  PHOTOS  ", restoreTarget: true as boolean | undefined } },
  );
  await waitFor(() => expect(result.current.locations).toEqual([first]));
  rerender({ query: "second", restoreTarget: false });
  expect(result.current.locations).toEqual([second]);
  rerender({ query: "SECOND", restoreTarget: true });
  expect(result.current.locations).toEqual([]);
  rerender({ query: "%_", restoreTarget: undefined });
  expect(result.current.locations).toEqual([literal]);
  rerender({ query: "", restoreTarget: undefined });
  expect(result.current.locations).toEqual([first, second, literal]);
  expect(list).toHaveBeenCalledOnce();
});

it("keeps an initial failure unknown through reopening and retries explicitly", async () => {
  list.mockReturnValueOnce({ response: Promise.reject(new Error("Offline")) });
  const { wrapper } = fixture();
  const { result, rerender } = renderHook(({ enabled }) => useLocationChoices({ enabled }), { wrapper, initialProps: { enabled: true } });
  await waitFor(() => expect(result.current.error).toBe("Offline"));
  expect(result.current.locations).toEqual([]);
  expect(result.current.loading).toBe(false);
  rerender({ enabled: false });
  rerender({ enabled: true });
  expect(result.current.error).toBe("Offline");
  expect(list).toHaveBeenCalledOnce();
  list.mockReturnValueOnce(call([first]));
  act(() => {
    result.current.retry();
    result.current.retry();
  });
  await waitFor(() => expect(result.current.locations).toEqual([first]));
  expect(result.current.error).toBe("");
  expect(list).toHaveBeenCalledTimes(2);
});

it("retains the complete known list during refresh and a later-page failure", async () => {
  const { store, wrapper } = fixture();
  list.mockReturnValueOnce(call([first]));
  await loadLocations(store);
  const { result } = renderHook(() => useLocationChoices({ enabled: true }), { wrapper });
  const pending = pendingPage();
  list.mockReturnValueOnce(call([second], true)).mockReturnValueOnce(pending);
  act(() => result.current.refresh());
  await waitFor(() => expect(list).toHaveBeenCalledTimes(3));
  expect(result.current.locations).toEqual([first]);
  await act(async () => pending.reject(new Error("Next page failed")));
  expect(result.current.locations).toEqual([first]);
  expect(result.current.error).toBe("Next page failed");
  list.mockReturnValueOnce(call([first], true)).mockReturnValueOnce(call([second]));
  act(() => result.current.retry());
  await waitFor(() => expect(result.current.locations).toEqual([first, second]));
});

it.each(["reply", "failure"])("ignores an older catalog %s after an explicit replacement refresh", async (outcome) => {
  const { store, wrapper } = fixture();
  list.mockReturnValueOnce(call([first]));
  await loadLocations(store);
  const { result } = renderHook(() => useLocationChoices({ enabled: true }), { wrapper });
  const old = pendingPage();
  const replacement = pendingPage();
  list.mockReturnValueOnce(old).mockReturnValueOnce(replacement);
  act(() => result.current.refresh());
  let refresh!: Promise<void>;
  act(() => {
    refresh = loadLocations(store, true);
  });
  expect(result.current.locations).toEqual([first]);
  expect(result.current.loading).toBe(true);
  await act(async () => {
    if (outcome === "reply") old.resolve({ locations: [first], hasMore: true });
    else old.reject(new Error("Stale failure"));
  });
  expect(result.current.loading).toBe(true);
  expect(result.current.error).toBe("");
  await act(async () => {
    replacement.resolve({ locations: [second], hasMore: false });
    await refresh;
  });
  expect(result.current.locations).toEqual([second]);
  expect(result.current.loading).toBe(false);
  expect(list).toHaveBeenCalledTimes(3);
});

it.each(["save", "remove"])("does not overwrite a confirmed %s with an older catalog reply", async (operation) => {
  const { store, wrapper } = fixture();
  list.mockReturnValueOnce(call([first]));
  await loadLocations(store);
  const { result } = renderHook(() => useLocationChoices({ enabled: true }), { wrapper });
  const pending = pendingPage();
  list.mockReturnValueOnce(pending);
  act(() => result.current.refresh());
  const updated = Location.create({ ...first, name: "Changed", restoreTarget: false });
  act(() => {
    if (operation === "save") saveLocation(store, updated);
    else removeLocation(store, first.id);
  });
  await act(async () => pending.resolve({ locations: [first], hasMore: false }));
  expect(result.current.locations).toEqual(operation === "save" ? [updated] : []);
  expect(result.current.loading).toBe(false);
});

it("keeps stores isolated and retains exact protobuf IDs and serializable state", async () => {
  const location = Location.create({ id: 9007199254740993n, revision: 9007199254740994n, name: "Exact" });
  list.mockReturnValueOnce(call([location]));
  const one = createAppStore();
  const two = createAppStore();
  await loadLocations(one);
  expect(selectLocations(one.getState())).toEqual([location]);
  expect(selectLocations(two.getState())).toEqual([]);
  expect(() => JSON.stringify(one.getState())).not.toThrow();
});

it.each(["save", "remove"])("finishes the full initial catalog after an overlapping %s", async (operation) => {
  const initial = pendingPage();
  const replacement = pendingPage();
  list.mockReturnValueOnce(initial).mockReturnValueOnce(replacement);
  const { store, wrapper } = fixture();
  const { result } = renderHook(() => useLocationChoices({ enabled: true }), { wrapper });
  const updated = Location.create({ ...first, name: "Saved name", restoreTarget: false });
  act(() => {
    if (operation === "save") saveLocation(store, updated);
    else removeLocation(store, first.id);
  });
  expect(list).toHaveBeenCalledTimes(2);
  expect(store.getState().locations.loaded).toBe(false);
  expect(result.current.loading).toBe(true);
  await act(async () => initial.resolve({ locations: [first], hasMore: true }));
  expect(result.current.locations).toEqual(operation === "save" ? [updated] : []);
  expect(result.current.loading).toBe(true);
  const complete = operation === "save" ? [updated, second] : [second];
  await act(async () => replacement.resolve({ locations: complete, hasMore: false }));
  expect(result.current.locations).toEqual(complete);
  expect(result.current.loading).toBe(false);
  expect(store.getState().locations.loaded).toBe(true);
  expect(list).toHaveBeenCalledTimes(2);
});

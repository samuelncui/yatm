import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { Location } from "@/entity";
import { useLocationChoices } from "./use-location-choices";

const { list } = vi.hoisted(() => ({ list: vi.fn() }));
vi.mock("@/api", () => ({ locationCli: { list } }));
const first = Location.create({ id: 1n, name: "First" });
const second = Location.create({ id: 2n, name: "Second" });
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

beforeEach(() => {
  list.mockReset().mockReturnValue(call([]));
});

it("serializes append requests, retains a failed page's rows and cursor, and deduplicates its retry", async () => {
  list.mockReturnValueOnce(call([first], true));
  const { result } = renderHook(() => useLocationChoices({ enabled: true, query: "First", restoreTarget: true }));
  await waitFor(() => expect(result.current.locations).toEqual([first]));
  const next = pendingPage();
  list.mockReturnValueOnce(next);
  act(() => {
    result.current.loadMore();
    result.current.loadMore();
  });
  expect(list).toHaveBeenCalledTimes(2);
  expect(list.mock.calls[1][0]).toEqual({ afterId: 1n, limit: 50, query: "First", restoreTarget: true });
  await act(async () => next.reject(new Error("Page unavailable")));
  expect(result.current.locations).toEqual([first]);
  expect(result.current.error).toBe("Page unavailable");
  act(() => result.current.loadMore());
  expect(list).toHaveBeenCalledTimes(2);

  const renamed = Location.create({ ...first, name: "Updated first" });
  list.mockReturnValueOnce(call([renamed, second]));
  act(() => {
    result.current.retry();
    result.current.retry();
  });
  await waitFor(() => expect(result.current.locations).toEqual([renamed, second]));
  expect(list).toHaveBeenCalledTimes(3);
  expect(list.mock.calls[2][0]).toEqual(list.mock.calls[1][0]);
  expect(result.current.error).toBe("");
  expect(result.current.more).toBe(false);
  expect(result.current.loading).toBe(false);
});

it("retries an initial failure from the first page", async () => {
  list.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Offline")) }));
  const { result } = renderHook(() => useLocationChoices({ enabled: true }));
  await waitFor(() => expect(result.current.error).toBe("Offline"));
  list.mockReturnValueOnce(call([first]));
  act(() => result.current.retry());
  await waitFor(() => expect(result.current.locations).toEqual([first]));
  expect(list.mock.calls.map(([request]) => request)).toEqual([
    { afterId: 0n, limit: 50, query: "" },
    { afterId: 0n, limit: 50, query: "" },
  ]);
});

it.each([false, true])("aborts a replaced query and cannot settle its successor with a late reply (failure: %s)", async (failed) => {
  const old = pendingPage();
  const current = pendingPage();
  list.mockReturnValueOnce(old).mockReturnValueOnce(current);
  const { result, rerender } = renderHook(({ query }) => useLocationChoices({ enabled: true, query }), { initialProps: { query: "old" } });
  const oldSignal = list.mock.calls[0][1].abort as AbortSignal;
  rerender({ query: "current" });
  expect(oldSignal.aborted).toBe(true);
  await act(async () => {
    if (failed) old.reject(new Error("Obsolete error"));
    else old.resolve({ locations: [first], hasMore: true });
  });
  expect(result.current.locations).toEqual([]);
  expect(result.current.error).toBe("");
  expect(result.current.loading).toBe(true);
  await act(async () => current.resolve({ locations: [second], hasMore: false }));
  expect(result.current.locations).toEqual([second]);
  expect(result.current.loading).toBe(false);
  expect(list.mock.calls[1][0]).toEqual({ afterId: 0n, limit: 50, query: "current" });
});

it("starts a separate first page when the preferred filter changes", async () => {
  const old = pendingPage();
  list.mockReturnValueOnce(old).mockReturnValueOnce(call([second]));
  const { result, rerender } = renderHook(({ restoreTarget }) => useLocationChoices({ enabled: true, restoreTarget }), {
    initialProps: { restoreTarget: false },
  });
  const signal = list.mock.calls[0][1].abort as AbortSignal;
  rerender({ restoreTarget: true });
  expect(signal.aborted).toBe(true);
  await waitFor(() => expect(result.current.locations).toEqual([second]));
  await act(async () => old.reject(new Error("Old filter failed")));
  expect(result.current.error).toBe("");
  expect(list.mock.calls[1][0]).toEqual({ afterId: 0n, limit: 50, query: "", restoreTarget: true });
});

it("stays idle when closed, starts afresh on reopening and cancels again on unmount", async () => {
  const old = pendingPage();
  const current = pendingPage();
  list.mockReturnValueOnce(old).mockReturnValueOnce(current);
  const { result, rerender, unmount } = renderHook(({ enabled }) => useLocationChoices({ enabled }), { initialProps: { enabled: false } });
  expect(list).not.toHaveBeenCalled();
  rerender({ enabled: true });
  const oldSignal = list.mock.calls[0][1].abort as AbortSignal;
  rerender({ enabled: false });
  expect(oldSignal.aborted).toBe(true);
  rerender({ enabled: true });
  await act(async () => old.resolve({ locations: [first], hasMore: true }));
  expect(result.current.locations).toEqual([]);
  expect(result.current.loading).toBe(true);
  const currentSignal = list.mock.calls[1][1].abort as AbortSignal;
  unmount();
  expect(currentSignal.aborted).toBe(true);
  await act(async () => current.reject(new Error("Closed request failed")));
  expect(list).toHaveBeenCalledTimes(2);
});

it("keeps simultaneous chooser requests independent", async () => {
  const firstRead = pendingPage();
  const secondRead = pendingPage();
  list.mockReturnValueOnce(firstRead).mockReturnValueOnce(secondRead);
  const one = renderHook(() => useLocationChoices({ enabled: true }));
  const two = renderHook(() => useLocationChoices({ enabled: true }));
  const [firstSignal, secondSignal] = list.mock.calls.map(([, options]) => options.abort as AbortSignal);
  one.unmount();
  expect(firstSignal.aborted).toBe(true);
  expect(secondSignal.aborted).toBe(false);
  await act(async () => {
    firstRead.reject(new Error("Closed chooser failed"));
    secondRead.resolve({ locations: [second], hasMore: false });
  });
  expect(two.result.current.locations).toEqual([second]);
  expect(two.result.current.error).toBe("");
});

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useJobProgress } from "./use-job-progress";

const deferred = <T>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((accept, fail) => {
    resolve = accept;
    reject = fail;
  });
  return { promise, resolve, reject };
};

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("shared Job progress polling", () => {
  it("waits for a slow request before scheduling another and retains data offscreen", async () => {
    const first = deferred<number>();
    const load = vi.fn((_signal: AbortSignal) => first.promise);
    const { result, rerender } = renderHook(({ visible }) => useJobProgress(1n, visible, load, "Progress failed"), { initialProps: { visible: true } });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10000);
    });
    expect(load).toHaveBeenCalledTimes(1);
    await act(async () => first.resolve(42));
    expect(result.current.data).toBe(42);
    rerender({ visible: false });
    expect(load.mock.calls[0][0].aborted).toBe(true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10000);
    });
    expect(load).toHaveBeenCalledTimes(1);
    expect(result.current.data).toBe(42);
  });

  it("ignores a late response for another Job and aborts on unmount", async () => {
    const old = deferred<number>();
    const latest = deferred<number>();
    const load = vi
      .fn((_signal: AbortSignal) => old.promise)
      .mockImplementationOnce(() => old.promise)
      .mockImplementationOnce(() => latest.promise);
    const { result, rerender, unmount } = renderHook(({ id }) => useJobProgress(id, true, load, "Progress failed"), { initialProps: { id: 1n } });
    rerender({ id: 2n });
    expect(load.mock.calls[0][0].aborted).toBe(true);
    await act(async () => latest.resolve(2));
    await act(async () => old.resolve(1));
    expect(result.current.data).toBe(2);
    unmount();
    expect(load.mock.calls[1][0].aborted).toBe(true);
  });

  it("keeps the last coherent snapshot on failure and recovers on the next poll", async () => {
    const load = vi
      .fn((_signal: AbortSignal) => Promise.resolve(1))
      .mockResolvedValueOnce(1)
      .mockRejectedValueOnce(new Error("Offline"))
      .mockResolvedValueOnce(2);
    const { result, unmount } = renderHook(() => useJobProgress(1n, true, load, "Progress failed"));
    await act(async () => {});
    expect(result.current.data).toBe(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(result.current).toEqual({ data: 1, error: "Offline" });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(result.current).toEqual({ data: 2, error: "" });
    unmount();
  });

  it("rejects a late stage window when the same Job starts a new request context", async () => {
    const old = deferred<{ window: string }>();
    const current = deferred<{ window: string }>();
    const previousLoad = vi.fn((_signal: AbortSignal) => old.promise);
    const nextLoad = vi.fn((_signal: AbortSignal) => current.promise);
    const { result, rerender } = renderHook(({ load }) => useJobProgress(1n, true, load, "Progress failed"), {
      initialProps: { load: previousLoad },
    });
    rerender({ load: nextLoad });
    expect(previousLoad.mock.calls[0][0].aborted).toBe(true);
    await act(async () => current.resolve({ window: "retry/preview" }));
    await act(async () => old.resolve({ window: "previous/read" }));
    expect(result.current.data).toEqual({ window: "retry/preview" });
  });
});

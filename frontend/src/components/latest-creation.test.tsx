import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { useLatestCreation } from "./latest-creation";

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

it("settles with the latest Job, and without one when the read fails", async () => {
  const found = renderHook(() => useLatestCreation(() => Promise.resolve("latest")));
  expect(found.result.current).toEqual({ settled: false });
  await act(() => vi.advanceTimersByTimeAsync(0));
  expect(found.result.current).toEqual({ settled: true, value: "latest" });

  const failed = renderHook(() => useLatestCreation<string>(() => Promise.reject(new Error("unavailable"))));
  await act(() => vi.advanceTimersByTimeAsync(0));
  expect(failed.result.current).toEqual({ settled: true });
});

it("reads nothing when disabled", () => {
  const load = vi.fn();
  expect(renderHook(() => useLatestCreation(load, false)).result.current).toEqual({ settled: true });
  expect(load).not.toHaveBeenCalled();
});

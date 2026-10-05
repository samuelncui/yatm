import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { importPositionsPreferenceKey, useLibraryAdmission } from "./import-positions-dialog";

const { importPositions, success } = vi.hoisted(() => ({ importPositions: vi.fn(), success: vi.fn() }));
vi.mock("@/api", () => ({ filesCli: { importPositions } }));
vi.mock("react-toastify", () => ({ toast: { success, info: vi.fn() } }));
const counts = { fileCount: 2n, directoryCount: 1n, existingCount: 0n, skippedFileCount: 0n };

beforeEach(() => {
  vi.resetAllMocks();
  localStorage.clear();
  sessionStorage.clear();
  importPositions.mockReturnValue({ response: Promise.resolve(counts) });
});
afterEach(() => vi.restoreAllMocks());

it("remembers the opt-out only after confirmed server success, using the existing local key and value", async () => {
  const view = renderHook(() => useLibraryAdmission(7n));
  act(() => view.result.current.ask());
  await waitFor(() => expect(view.result.current.canConfirm).toBe(true));
  act(() => view.result.current.setSkipPrompt(true));
  expect(localStorage.getItem(importPositionsPreferenceKey)).toBeNull();
  let complete!: (reply: unknown) => void;
  importPositions.mockReturnValueOnce({
    response: new Promise((resolve) => {
      complete = resolve;
    }),
  });
  act(() => view.result.current.confirm());
  expect(localStorage.getItem(importPositionsPreferenceKey)).toBeNull();
  await act(async () => complete(counts));
  expect(view.result.current.open).toBe(false);
  expect(localStorage.getItem(importPositionsPreferenceKey)).toBe("1");
  expect(sessionStorage.getItem(importPositionsPreferenceKey)).toBeNull();
  importPositions.mockClear();
  act(() => view.result.current.ask());
  await waitFor(() => expect(view.result.current.open).toBe(false));
  expect(importPositions).toHaveBeenCalledExactlyOnceWith({ positionIds: [], mediaId: 7n, dryrun: false });
});

it("reports successful admission even when the preference write fails", async () => {
  const view = renderHook(() => useLibraryAdmission(7n));
  act(() => view.result.current.ask());
  await waitFor(() => expect(view.result.current.canConfirm).toBe(true));
  act(() => view.result.current.setSkipPrompt(true));
  const write = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new Error("Storage denied");
  });
  act(() => view.result.current.confirm());
  await waitFor(() => expect(view.result.current.open).toBe(false));
  expect(view.result.current.error).toBe("");
  expect(success).toHaveBeenCalledExactlyOnceWith("Added 2 Files to Library");
  expect(write).toHaveBeenCalledExactlyOnceWith(importPositionsPreferenceKey, "1");
});

it.each(["cancel", "failed import", "unchecked"] as const)("does not commit the opt-out for %s", async (outcome) => {
  const view = renderHook(() => useLibraryAdmission(7n));
  act(() => view.result.current.ask());
  await waitFor(() => expect(view.result.current.canConfirm).toBe(true));
  act(() => view.result.current.setSkipPrompt(outcome !== "unchecked"));
  const write = vi.spyOn(Storage.prototype, "setItem");
  if (outcome === "cancel") act(() => view.result.current.close());
  else {
    if (outcome === "failed import") importPositions.mockReturnValueOnce({ response: Promise.reject(new Error("Import offline")) });
    act(() => view.result.current.confirm());
    await waitFor(() => expect(view.result.current.busy).toBe(false));
  }
  expect(write).not.toHaveBeenCalled();
  expect(localStorage.getItem(importPositionsPreferenceKey)).toBeNull();
  if (outcome === "failed import") {
    expect(view.result.current.error).toBe("Import offline");
    expect(view.result.current.skipPrompt).toBe(true);
    expect(success).not.toHaveBeenCalled();
  }
});

it("falls back to confirmation when preference access fails and discards a cancelled dry-run reply", async () => {
  vi.spyOn(window, "localStorage", "get").mockImplementation(() => {
    throw new Error("Storage unavailable");
  });
  let complete!: (reply: unknown) => void;
  importPositions.mockReturnValueOnce({
    response: new Promise((resolve) => {
      complete = resolve;
    }),
  });
  const view = renderHook(() => useLibraryAdmission(7n));
  act(() => view.result.current.ask());
  expect(view.result.current.calculating).toBe(true);
  expect(importPositions.mock.calls[0][0]).toEqual({ positionIds: [], mediaId: 7n, dryrun: true });
  const signal = importPositions.mock.calls[0][1].abort as AbortSignal;
  act(() => view.result.current.close());
  expect(signal.aborted).toBe(true);
  await act(async () => complete(counts));
  expect(view.result.current.open).toBe(false);
  expect(success).not.toHaveBeenCalled();
});

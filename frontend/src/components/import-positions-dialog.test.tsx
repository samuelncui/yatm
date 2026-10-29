import { act, render, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { admissionToast, ImportPositionsDialog, importPositionsPreferenceKey, useLibraryAdmission } from "./import-positions-dialog";

const { importPositions, success, info } = vi.hoisted(() => ({ importPositions: vi.fn(), success: vi.fn(), info: vi.fn() }));
vi.mock("@/api", () => ({ filesCli: { importPositions } }));
vi.mock("react-toastify", () => ({ toast: { success, info } }));
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

it.each([
  [0n, 0n, 0n, "No recorded files to add."],
  [0n, 3n, 0n, "No new Files to add: 3 already in Library."],
  [0n, 0n, 4n, "No new Files to add: 4 without signatures."],
  [0n, 3n, 4n, "No new Files to add: 3 already in Library; 4 without signatures."],
  [2n, 3n, 4n, "Added 2 Files to Library; skipped 3 already in Library; 4 without signatures"],
] as const)("reports added=%s, existing=%s and unsigned=%s distinctly", (files, existing, skipped, message) => {
  admissionToast(files, existing, skipped);
  expect(files > 0n ? success : info).toHaveBeenCalledExactlyOnceWith(message);
  expect(files > 0n ? info : success).not.toHaveBeenCalled();
});

it("reviews even after opting out and restores confirmation when there is nothing to add", async () => {
  localStorage.setItem(importPositionsPreferenceKey, "1");
  importPositions.mockReturnValue({ response: Promise.resolve({ ...counts, fileCount: 0n, skippedFileCount: 4n }) });
  const Harness = () => {
    const admission = useLibraryAdmission(7n);
    return (
      <>
        <button onClick={admission.ask}>Add existing inventory</button>
        <button onClick={admission.review}>Review what will be added</button>
        <ImportPositionsDialog admission={admission} />
      </>
    );
  };
  render(<Harness />);
  await userEvent.click(screen.getByRole("button", { name: "Review what will be added" }));
  expect(await screen.findByText("1 folder entries · 4 without signatures")).toBeVisible();
  expect(screen.queryByText("No recorded files to add.")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Add to Library" })).toBeDisabled();
  expect(importPositions).toHaveBeenCalledExactlyOnceWith({ positionIds: [], mediaId: 7n, dryrun: true }, { abort: expect.any(AbortSignal) });
  expect(localStorage.getItem(importPositionsPreferenceKey)).toBe("1");
  await userEvent.click(screen.getByRole("button", { name: "Show confirmation next time" }));
  expect(localStorage.getItem(importPositionsPreferenceKey)).toBeNull();
  expect(screen.queryByRole("button", { name: "Show confirmation next time" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  await userEvent.click(screen.getByRole("button", { name: "Add existing inventory" }));
  await screen.findByText("1 folder entries · 4 without signatures");
  expect(importPositions).toHaveBeenCalledTimes(2);
  expect(importPositions.mock.calls.every(([request]) => request.dryrun)).toBe(true);
});

it("keeps the opt-out when review is cancelled and does not misreport a failed preference write", async () => {
  localStorage.setItem(importPositionsPreferenceKey, "1");
  const view = renderHook(() => useLibraryAdmission(7n));
  act(() => view.result.current.review());
  await waitFor(() => expect(view.result.current.canConfirm).toBe(true));
  expect(view.result.current.skipPrompt).toBe(true);
  vi.spyOn(Storage.prototype, "removeItem").mockImplementation(() => {
    throw new Error("Storage denied");
  });
  act(() => view.result.current.restorePrompt());
  expect(info).toHaveBeenCalledWith("Could not save this browser preference.");
  expect(view.result.current.promptSuppressed).toBe(true);
  act(() => view.result.current.close());
  expect(localStorage.getItem(importPositionsPreferenceKey)).toBe("1");
  expect(importPositions).toHaveBeenCalledTimes(1);
});

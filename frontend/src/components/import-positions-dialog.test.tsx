import { act, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { PropsWithChildren } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { GeneralSettings, SettingsGroup, SettingsValue } from "@/entity";
import { AppStateProvider } from "@/state/react";
import { createAppStore } from "@/state/store";
import { render } from "@/state/test-render";
import { admissionToast, ImportPositionsDialog, useLibraryAdmission } from "./import-positions-dialog";
import { loadCommittedSettings } from "./settings-editor";

const { importPositions, get, update, success, info } = vi.hoisted(() => ({
  importPositions: vi.fn(),
  get: vi.fn(),
  update: vi.fn(),
  success: vi.fn(),
  info: vi.fn(),
}));
vi.mock("@/api", () => ({ filesCli: { importPositions }, settingsCli: { get, update } }));
vi.mock("react-toastify", () => ({ toast: { success, info } }));
const counts = { fileCount: 2n, directoryCount: 1n, existingCount: 0n, skippedFileCount: 0n };
const stored = (skipAddMediaConfirmation: boolean) => GeneralSettings.create({ confirmRemove: true, skipAddMediaConfirmation });
const reply = (skipAddMediaConfirmation: boolean) => ({
  response: Promise.resolve({ value: SettingsValue.create({ value: { oneofKind: "general", general: stored(skipAddMediaConfirmation) } }) }),
});
const saved = (skipAddMediaConfirmation: boolean) => ({ value: { value: { oneofKind: "general", general: stored(skipAddMediaConfirmation) } } });

/** A store that already holds the server's Library settings, so the first click sees the stored choice. */
const storeWith = async (skipAddMediaConfirmation: boolean) => {
  get.mockReturnValue(reply(skipAddMediaConfirmation));
  const store = createAppStore();
  await loadCommittedSettings(store, SettingsGroup.GENERAL);
  get.mockClear();
  return store;
};
const admit = async (skipAddMediaConfirmation = false) => {
  const store = await storeWith(skipAddMediaConfirmation);
  const wrapper = ({ children }: PropsWithChildren) => <AppStateProvider store={store}>{children}</AppStateProvider>;
  return renderHook(() => useLibraryAdmission(7n), { wrapper });
};

beforeEach(() => {
  vi.resetAllMocks();
  localStorage.clear();
  sessionStorage.clear();
  importPositions.mockReturnValue({ response: Promise.resolve(counts) });
  update.mockImplementation(({ value }: { value: SettingsValue }) => ({ response: Promise.resolve({ value: SettingsValue.clone(value) }) }));
});
afterEach(() => expect(localStorage).toHaveLength(0));

it("stores the opt-out in Library settings only after confirmed server success", async () => {
  const view = await admit();
  act(() => view.result.current.ask());
  await waitFor(() => expect(view.result.current.canConfirm).toBe(true));
  act(() => view.result.current.setSkipPrompt(true));
  let complete!: (reply: unknown) => void;
  importPositions.mockReturnValueOnce({
    response: new Promise((resolve) => {
      complete = resolve;
    }),
  });
  act(() => view.result.current.confirm());
  expect(view.result.current.busy).toBe(true);
  expect(get).not.toHaveBeenCalled();
  expect(update).not.toHaveBeenCalled();
  await act(async () => complete(counts));
  await waitFor(() => expect(view.result.current.open).toBe(false));
  // The save starts from a fresh server read and replaces the whole group.
  expect(get).toHaveBeenCalledExactlyOnceWith({ group: SettingsGroup.GENERAL });
  expect(update).toHaveBeenCalledExactlyOnceWith(saved(true));
  expect(info).not.toHaveBeenCalled();
  importPositions.mockClear();
  act(() => view.result.current.ask());
  await waitFor(() => expect(success).toHaveBeenCalledTimes(2));
  expect(view.result.current.open).toBe(false);
  expect(importPositions).toHaveBeenCalledExactlyOnceWith({ positionIds: [], mediaId: 7n, dryrun: false });
  expect(update).toHaveBeenCalledOnce();
});

it("reports successful admission even when the choice cannot be saved", async () => {
  const view = await admit();
  act(() => view.result.current.ask());
  await waitFor(() => expect(view.result.current.canConfirm).toBe(true));
  act(() => view.result.current.setSkipPrompt(true));
  update.mockReturnValue({ response: Promise.reject(new Error("Settings unavailable")) });
  act(() => view.result.current.confirm());
  await waitFor(() => expect(view.result.current.open).toBe(false));
  expect(view.result.current.error).toBe("");
  expect(success).toHaveBeenCalledExactlyOnceWith("Added 2 Files to Library");
  expect(info).toHaveBeenCalledExactlyOnceWith("Could not save this choice.");
  expect(update).toHaveBeenCalledExactlyOnceWith(saved(true));
  // The confirmation stays as it was.
  importPositions.mockClear();
  act(() => view.result.current.ask());
  await waitFor(() => expect(view.result.current.canConfirm).toBe(true));
  expect(importPositions.mock.calls[0][0]).toEqual({ positionIds: [], mediaId: 7n, dryrun: true });
});

it.each(["cancel", "failed import", "unchecked"] as const)("does not commit the opt-out for %s", async (outcome) => {
  const view = await admit();
  act(() => view.result.current.ask());
  await waitFor(() => expect(view.result.current.canConfirm).toBe(true));
  act(() => view.result.current.setSkipPrompt(outcome !== "unchecked"));
  if (outcome === "cancel") act(() => view.result.current.close());
  else {
    if (outcome === "failed import") importPositions.mockReturnValueOnce({ response: Promise.reject(new Error("Import offline")) });
    act(() => view.result.current.confirm());
    await waitFor(() => expect(view.result.current.busy).toBe(false));
  }
  expect(get).not.toHaveBeenCalled();
  expect(update).not.toHaveBeenCalled();
  if (outcome === "failed import") {
    expect(view.result.current.error).toBe("Import offline");
    expect(view.result.current.skipPrompt).toBe(true);
    expect(success).not.toHaveBeenCalled();
  }
});

it("falls back to confirmation when the settings read fails and discards a cancelled dry-run reply", async () => {
  get.mockReturnValue({ response: Promise.reject(new Error("Settings unavailable")) });
  let complete!: (reply: unknown) => void;
  importPositions.mockReturnValueOnce({
    response: new Promise((resolve) => {
      complete = resolve;
    }),
  });
  const view = renderHook(() => useLibraryAdmission(7n), { wrapper: AppStateProvider });
  await waitFor(() => expect(get).toHaveBeenCalledOnce());
  await act(async () => {});
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
  const store = await storeWith(true);
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
  render(<Harness />, { store });
  await userEvent.click(screen.getByRole("button", { name: "Review what will be added" }));
  expect(await screen.findByText("1 folder entries · 4 without signatures")).toBeVisible();
  expect(screen.queryByText("No recorded files to add.")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Add to Library" })).toBeDisabled();
  expect(screen.getByRole("checkbox", { name: "Don’t show this again" })).toBeChecked();
  expect(importPositions).toHaveBeenCalledExactlyOnceWith({ positionIds: [], mediaId: 7n, dryrun: true }, { abort: expect.any(AbortSignal) });
  expect(update).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Show confirmation next time" }));
  await waitFor(() => expect(screen.queryByRole("button", { name: "Show confirmation next time" })).not.toBeInTheDocument());
  expect(update).toHaveBeenCalledExactlyOnceWith(saved(false));
  expect(screen.getByRole("checkbox", { name: "Don’t show this again" })).not.toBeChecked();
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  await userEvent.click(screen.getByRole("button", { name: "Add existing inventory" }));
  await screen.findByText("1 folder entries · 4 without signatures");
  expect(importPositions).toHaveBeenCalledTimes(2);
  expect(importPositions.mock.calls.every(([request]) => request.dryrun)).toBe(true);
});

it("keeps the opt-out when review is cancelled and does not misreport a failed save", async () => {
  const view = await admit(true);
  act(() => view.result.current.review());
  await waitFor(() => expect(view.result.current.canConfirm).toBe(true));
  expect(view.result.current.skipPrompt).toBe(true);
  update.mockReturnValue({ response: Promise.reject(new Error("Settings unavailable")) });
  act(() => view.result.current.restorePrompt());
  await waitFor(() => expect(info).toHaveBeenCalledExactlyOnceWith("Could not save this choice."));
  expect(view.result.current.promptSuppressed).toBe(true);
  expect(view.result.current.skipPrompt).toBe(true);
  act(() => view.result.current.close());
  expect(importPositions).toHaveBeenCalledTimes(1);
  // The stored choice still skips the confirmation.
  act(() => view.result.current.ask());
  await waitFor(() => expect(success).toHaveBeenCalledOnce());
  expect(importPositions).toHaveBeenLastCalledWith({ positionIds: [], mediaId: 7n, dryrun: false });
});

it.each([
  [true, undefined],
  [false, saved(false)],
] as const)("saves a reviewed choice only when it differs from the stored one (ticked=%s)", async (ticked, request) => {
  const view = await admit(true);
  act(() => view.result.current.review());
  await waitFor(() => expect(view.result.current.canConfirm).toBe(true));
  act(() => view.result.current.setSkipPrompt(ticked));
  act(() => view.result.current.confirm());
  await waitFor(() => expect(view.result.current.open).toBe(false));
  expect(success).toHaveBeenCalledExactlyOnceWith("Added 2 Files to Library");
  if (request) expect(update).toHaveBeenCalledExactlyOnceWith(request);
  else expect(update).not.toHaveBeenCalled();
});

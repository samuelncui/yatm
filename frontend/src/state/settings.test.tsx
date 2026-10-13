import { act, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { JobSettings, LibrarySettings, SettingsGroup, SettingsValue } from "@/entity";
import { loadCommittedSettings, useCommittedSettings, useSettingsEditor } from "@/components/settings-editor";
import { AppStateProvider } from "./react";
import { createAppStore } from "./store";
import { makeSelectSettings, settingsActions } from "./settings";

const { get, update } = vi.hoisted(() => ({ get: vi.fn(), update: vi.fn() }));
vi.mock("@/api", () => ({ settingsCli: { get, update } }));
const value = (confirmRemove: boolean) => SettingsValue.create({ value: { oneofKind: "library", library: LibrarySettings.create({ confirmRemove }) } });
const Reader = () => {
  const { settings } = useCommittedSettings(SettingsGroup.LIBRARY);
  return <output>Committed:{String(settings?.confirmRemove)}</output>;
};
const Editor = () => {
  const editor = useSettingsEditor(SettingsGroup.LIBRARY, LibrarySettings, "Library");
  return (
    <>
      <output>Draft:{String(editor.draft?.confirmRemove)}</output>
      <button onClick={() => editor.setDraft((draft) => draft && { ...draft, confirmRemove: false })}>Edit</button>
      <button onClick={() => void editor.save()}>Save</button>
      <button onClick={editor.cancel}>Cancel</button>
    </>
  );
};
beforeEach(() => {
  sessionStorage.clear();
  get.mockReset();
  update.mockReset();
});

it("shares an in-flight Settings read but keeps edits private until the authoritative Save response", async () => {
  get.mockReturnValue({ response: Promise.resolve({ value: value(true) }) });
  update.mockReturnValue({ response: Promise.resolve({ value: value(false) }) });
  render(
    <AppStateProvider>
      <Reader />
      <Reader />
      <Editor />
    </AppStateProvider>,
  );
  await screen.findByText("Draft:true");
  expect(get).toHaveBeenCalledOnce();
  fireEvent.click(screen.getByText("Edit"));
  expect(screen.getAllByText("Committed:true")).toHaveLength(2);
  fireEvent.click(screen.getByText("Cancel"));
  expect(screen.getByText("Draft:true")).toBeVisible();
  fireEvent.click(screen.getByText("Edit"));
  fireEvent.click(screen.getByText("Save"));
  await waitFor(() => expect(screen.getAllByText("Committed:false")).toHaveLength(2));
  expect(update).toHaveBeenCalledOnce();
});

it("does not let an older Get overwrite Save or its editor draft", async () => {
  get.mockReturnValueOnce({ response: Promise.resolve({ value: value(true) }) });
  update.mockReturnValue({ response: Promise.resolve({ value: value(false) }) });
  const store = createAppStore();
  render(
    <AppStateProvider store={store}>
      <Reader />
      <Editor />
    </AppStateProvider>,
  );
  await screen.findByText("Draft:true");
  let resolve!: (reply: { value: SettingsValue }) => void;
  get.mockReturnValueOnce({
    response: new Promise((done) => {
      resolve = done;
    }),
  });
  let reading!: ReturnType<typeof loadCommittedSettings>;
  act(() => {
    reading = loadCommittedSettings(store, SettingsGroup.LIBRARY, true);
  });
  fireEvent.click(screen.getByText("Edit"));
  fireEvent.click(screen.getByText("Save"));
  await screen.findByText("Committed:false");
  await act(async () => {
    resolve({ value: value(true) });
    await reading;
  });
  expect(screen.getByText("Committed:false")).toBeVisible();
  expect(screen.getByText("Draft:false")).toBeVisible();
  expect(makeSelectSettings(SettingsGroup.LIBRARY)(store.getState())).toEqual(value(false));
});

it("keeps missing Settings unknown after a failed read", async () => {
  get.mockReturnValue({ response: Promise.reject(new Error("Unavailable")) });
  const store = createAppStore();
  await expect(loadCommittedSettings(store, SettingsGroup.LIBRARY)).rejects.toThrow("Unavailable");
  expect(store.getState().settings[SettingsGroup.LIBRARY]).toMatchObject({ loading: false, error: "Unavailable" });
  expect(makeSelectSettings(SettingsGroup.LIBRARY)(store.getState())).toBeUndefined();
});

it("shares Job Settings reads and opens its cached editor immediately without refetching", async () => {
  const job = JobSettings.create({ execution: { readBatch: 256 } });
  const reply = SettingsValue.create({ value: { oneofKind: "job", job } });
  get.mockReturnValue({ response: Promise.resolve({ value: reply }) });
  const store = createAppStore();
  await Promise.all([loadCommittedSettings(store, SettingsGroup.JOB), loadCommittedSettings(store, SettingsGroup.JOB)]);
  const wrapper = ({ children }: { children: React.ReactNode }) => <AppStateProvider store={store}>{children}</AppStateProvider>;
  const { result } = renderHook(() => useSettingsEditor(SettingsGroup.JOB, JobSettings, "Job"), { wrapper });
  expect(result.current.loading).toBe(false);
  expect(result.current.draft).toEqual(job);
  await act(async () => {});
  expect(get).toHaveBeenCalledOnce();
});

it("adopts a Save completed after navigating away and back without refetching", async () => {
  get.mockReturnValue({ response: Promise.resolve({ value: value(true) }) });
  let finish!: (reply: { value: SettingsValue }) => void;
  update.mockReturnValue({ response: new Promise((resolve) => (finish = resolve)) });
  const store = createAppStore();
  const wrapper = ({ children }: { children: React.ReactNode }) => <AppStateProvider store={store}>{children}</AppStateProvider>;
  const first = renderHook(() => useSettingsEditor(SettingsGroup.LIBRARY, LibrarySettings, "Library"), { wrapper });
  await waitFor(() => expect(first.result.current.loading).toBe(false));
  act(() => first.result.current.setDraft((draft) => draft && { ...draft, confirmRemove: false }));
  let saving!: Promise<boolean>;
  act(() => {
    saving = first.result.current.save();
  });
  first.unmount();
  const next = renderHook(() => useSettingsEditor(SettingsGroup.LIBRARY, LibrarySettings, "Library"), { wrapper });
  expect(next.result.current.draft?.confirmRemove).toBe(true);
  expect(next.result.current.loading).toBe(false);
  await act(async () => {
    finish({ value: value(false) });
    await saving;
  });
  expect(next.result.current.draft?.confirmRemove).toBe(false);
  expect(next.result.current.dirty).toBe(false);
  expect(get).toHaveBeenCalledOnce();
});

it("preserves a dirty draft when committed values change and cancels to the newest value", async () => {
  get.mockReturnValue({ response: Promise.resolve({ value: value(true) }) });
  const store = createAppStore();
  const wrapper = ({ children }: { children: React.ReactNode }) => <AppStateProvider store={store}>{children}</AppStateProvider>;
  const { result } = renderHook(() => useSettingsEditor(SettingsGroup.LIBRARY, LibrarySettings, "Library"), { wrapper });
  await waitFor(() => expect(result.current.loading).toBe(false));
  act(() => result.current.setDraft((draft) => draft && { ...draft, includeUnbackedFiles: true }));
  const edited = result.current.draft;
  act(() => store.dispatch(settingsActions.saved({ group: SettingsGroup.LIBRARY, value: SettingsValue.toJsonString(value(false)) })));
  expect(result.current.draft).toEqual(edited);
  expect(result.current.dirty).toBe(true);
  act(() => result.current.cancel());
  expect(result.current.draft?.confirmRemove).toBe(false);
  expect(result.current.dirty).toBe(false);
});

it("retains a failed Settings read across consumers until explicit Retry", async () => {
  get.mockReturnValueOnce({ response: Promise.reject(new Error("Unavailable")) });
  const store = createAppStore();
  const wrapper = ({ children }: { children: React.ReactNode }) => <AppStateProvider store={store}>{children}</AppStateProvider>;
  const first = renderHook(() => useSettingsEditor(SettingsGroup.LIBRARY, LibrarySettings, "Library"), { wrapper });
  await waitFor(() => expect(first.result.current.error).toBe("Unavailable"));
  first.unmount();
  get.mockReturnValue({ response: Promise.resolve({ value: value(true) }) });
  const next = renderHook(() => useSettingsEditor(SettingsGroup.LIBRARY, LibrarySettings, "Library"), { wrapper });
  const reader = renderHook(() => useCommittedSettings(SettingsGroup.LIBRARY), { wrapper });
  await act(async () => {});
  expect(get).toHaveBeenCalledOnce();
  expect(next.result.current.draft).toBeUndefined();
  expect(next.result.current.error).toBe("Unavailable");
  expect(reader.result.current.settings).toBeUndefined();
  expect(reader.result.current.loading).toBe(false);
  act(() => next.result.current.reload());
  await waitFor(() => expect(next.result.current.draft?.confirmRemove).toBe(true));
  expect(next.result.current.error).toBe("");
  expect(get).toHaveBeenCalledTimes(2);
});

it.each(["success", "failure"])("does not settle an editor from a superseded initial read (%s)", async (outcome) => {
  let finishOld!: (reply: { value: SettingsValue }) => void;
  let failOld!: (error: Error) => void;
  let finishNew!: (reply: { value: SettingsValue }) => void;
  get.mockReturnValueOnce({
    response: new Promise((resolve, reject) => {
      finishOld = resolve;
      failOld = reject;
    }),
  });
  get.mockReturnValueOnce({ response: new Promise((resolve) => (finishNew = resolve)) });
  const store = createAppStore();
  const wrapper = ({ children }: { children: React.ReactNode }) => <AppStateProvider store={store}>{children}</AppStateProvider>;
  const { result } = renderHook(() => useSettingsEditor(SettingsGroup.LIBRARY, LibrarySettings, "Library"), { wrapper });
  let latest!: ReturnType<typeof loadCommittedSettings>;
  act(() => {
    latest = loadCommittedSettings(store, SettingsGroup.LIBRARY, true);
  });
  await act(async () => {
    if (outcome === "success") finishOld({ value: value(true) });
    else failOld(new Error("Obsolete failure"));
  });
  expect(result.current.draft).toBeUndefined();
  expect(result.current.error).toBe("");
  expect(result.current.loading).toBe(true);
  await act(async () => {
    finishNew({ value: value(false) });
    await latest;
  });
  expect(result.current.draft?.confirmRemove).toBe(false);
  expect(result.current.loading).toBe(false);
  expect(get).toHaveBeenCalledTimes(2);
});

it("keeps an explicit reload pending when another reload supersedes it over cached Settings", async () => {
  get.mockReturnValueOnce({ response: Promise.resolve({ value: value(true) }) });
  const store = createAppStore();
  const wrapper = ({ children }: { children: React.ReactNode }) => <AppStateProvider store={store}>{children}</AppStateProvider>;
  const { result } = renderHook(() => useSettingsEditor(SettingsGroup.LIBRARY, LibrarySettings, "Library"), { wrapper });
  await waitFor(() => expect(result.current.loading).toBe(false));
  let finishOld!: (reply: { value: SettingsValue }) => void;
  let finishNew!: (reply: { value: SettingsValue }) => void;
  get.mockReturnValueOnce({ response: new Promise((resolve) => (finishOld = resolve)) });
  get.mockReturnValueOnce({ response: new Promise((resolve) => (finishNew = resolve)) });
  act(() => result.current.reload());
  let latest!: ReturnType<typeof loadCommittedSettings>;
  act(() => {
    latest = loadCommittedSettings(store, SettingsGroup.LIBRARY, true);
  });
  await act(async () => finishOld({ value: value(true) }));
  expect(result.current.loading).toBe(true);
  expect(result.current.draft?.confirmRemove).toBe(true);
  await act(async () => {
    finishNew({ value: value(false) });
    await latest;
  });
  expect(result.current.loading).toBe(false);
  expect(result.current.draft?.confirmRemove).toBe(false);
  expect(result.current.dirty).toBe(false);
});

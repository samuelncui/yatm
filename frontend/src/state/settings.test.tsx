import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { LibrarySettings, SettingsGroup, SettingsValue } from "@/entity";
import { loadCommittedSettings, useCommittedSettings, useSettingsEditor } from "@/components/settings-editor";
import { AppStateProvider } from "./react";
import { createAppStore } from "./store";
import { makeSelectSettings } from "./settings";

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

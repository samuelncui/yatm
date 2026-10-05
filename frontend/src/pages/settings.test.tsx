import { screen, waitFor } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router";
import { LibrarySettings, SettingsValue } from "@/entity";

const { get, update } = vi.hoisted(() => ({ get: vi.fn(), update: vi.fn() }));
vi.mock("@/api", () => ({ fileBase: "/files", settingsCli: { get, update } }));
import { SettingsBrowser } from "./settings";

const initial = LibrarySettings.create({ includeUnbackedFiles: true, confirmRemove: true });
const value = (settings = initial) => SettingsValue.create({ value: { oneofKind: "library", library: LibrarySettings.clone(settings) } });
const open = () =>
  render(
    <MemoryRouter>
      <SettingsBrowser />
    </MemoryRouter>,
  );

beforeEach(() => {
  get.mockReset().mockReturnValue({ response: Promise.resolve({ value: value() }) });
  update
    .mockReset()
    .mockImplementation(({ value: settings }: { value: SettingsValue }) => ({ response: Promise.resolve({ value: SettingsValue.clone(settings) }) }));
  vi.unstubAllGlobals();
});

describe("Library Settings", () => {
  it("edits locally and saves the whole typed group", async () => {
    open();
    const toggle = screen.getByRole("checkbox", { name: "Include unbacked files" });
    await waitFor(() => expect(toggle).toBeEnabled());
    await userEvent.click(toggle);
    expect(update).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(update).toHaveBeenCalledExactlyOnceWith({
      value: { value: { oneofKind: "library", library: { ...initial, includeUnbackedFiles: false } } },
    });
    expect(await screen.findByText("Library settings saved.")).toBeVisible();
    expect(screen.getByText(/Locations are unaffected/)).toBeInTheDocument();
    const actions = screen.getByRole("button", { name: "Save" }).parentElement;
    expect(Array.from(actions!.children, (action) => action.textContent?.trim())).toEqual(["Save", "Cancel", "Import", "Export"]);
  });

  it("cancels unsaved Trash confirmation changes", async () => {
    open();
    const toggle = screen.getByRole("checkbox", { name: "Confirm Delete" });
    await waitFor(() => expect(toggle).toBeEnabled());
    await userEvent.click(toggle);
    expect(toggle).not.toBeChecked();
    expect(screen.getByText("Delete moves selected items to Trash immediately.")).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(toggle).toBeChecked();
    expect(update).not.toHaveBeenCalled();
  });

  it("locks editable controls during Save and retains the draft and error after failure", async () => {
    let reject!: (error: Error) => void;
    update.mockImplementation(() => ({ response: new Promise((_, fail) => (reject = fail)) }));
    open();
    const toggle = screen.getByRole("checkbox", { name: "Confirm Delete" });
    await waitFor(() => expect(toggle).toBeEnabled());
    await userEvent.click(toggle);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    for (const input of screen.getAllByRole("checkbox")) expect(input).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    reject(new Error("Settings unavailable"));
    expect(await screen.findByText("Settings unavailable")).toBeVisible();
    expect(toggle).not.toBeChecked();
    expect(toggle).toBeEnabled();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(screen.getByText("Settings unavailable")).toBeVisible();
    expect(screen.getByRole("button", { name: "Reload saved settings" })).toBeDisabled();
    reject(new Error("Still unavailable"));
    expect(await screen.findByText("Still unavailable")).toBeVisible();
    expect(toggle).not.toBeChecked();
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
  });

  it("does not invent defaults while the server value is loading", () => {
    get.mockReturnValue({ response: new Promise(() => undefined) });
    open();
    expect(screen.getByRole("checkbox", { name: "Include unbacked files" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Include unbacked files" })).toBeDisabled();
    expect(screen.getByRole("checkbox", { name: "Confirm Delete" })).not.toBeChecked();
  });

  it("reports Library import success and keeps Settings explicitly out of the operation", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetch);
    open();
    await userEvent.click(screen.getByRole("button", { name: "Import" }));
    expect(screen.getByText("Import replaces Library data. Your Settings stay unchanged.")).toBeVisible();
    const input = document.querySelector<HTMLInputElement>('input[type="file"]');
    expect(input).not.toBeNull();
    await userEvent.upload(input!, new File(["backup"], "library.jsonl", { type: "application/x-ndjson" }));
    await userEvent.click(screen.getByRole("button", { name: "Import" }));
    expect(await screen.findByText("Library imported. Settings were not changed.")).toBeVisible();
    expect(fetch).toHaveBeenCalledWith("/files/library/_import", expect.objectContaining({ method: "POST" }));
  });

  it("keeps the import dialog open while pending and reports a failure", async () => {
    let reject!: (failure: Error) => void;
    vi.stubGlobal(
      "fetch",
      vi.fn().mockReturnValue(
        new Promise((_, fail) => {
          reject = fail;
        }),
      ),
    );
    open();
    await userEvent.click(screen.getByRole("button", { name: "Import" }));
    const input = document.querySelector<HTMLInputElement>('input[type="file"]');
    await userEvent.upload(input!, new File(["backup"], "library.jsonl", { type: "application/x-ndjson" }));
    await userEvent.click(screen.getByRole("button", { name: "Import" }));
    expect(screen.getByRole("progressbar", { name: "Importing Library backup" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    reject(new Error("Import unavailable"));
    expect(await screen.findByText("Import unavailable")).toBeVisible();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled();
  });
});

import { act, screen, waitFor } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router";
import { LibrarySettings, Location, SettingsValue } from "@/entity";
import { createAppStore } from "@/state/store";
import { loadLocations, selectLocations } from "@/state/locations";
import { useLocationChoices } from "@/components/use-location-choices";

const { get, update, list } = vi.hoisted(() => ({ get: vi.fn(), update: vi.fn(), list: vi.fn() }));
vi.mock("@/api", () => ({ fileBase: "/files", settingsCli: { get, update }, locationCli: { list } }));
import { SettingsBrowser } from "./settings";

const initial = LibrarySettings.create({ includeFilesWithoutVersions: true, confirmRemove: true });
const value = (settings = initial) => SettingsValue.create({ value: { oneofKind: "library", library: LibrarySettings.clone(settings) } });
const open = () =>
  render(
    <MemoryRouter>
      <SettingsBrowser />
    </MemoryRouter>,
  );

beforeEach(() => {
  list.mockReset().mockReturnValue({ response: Promise.resolve({ locations: [], hasMore: false }) });
  get.mockReset().mockReturnValue({ response: Promise.resolve({ value: value() }) });
  update
    .mockReset()
    .mockImplementation(({ value: settings }: { value: SettingsValue }) => ({ response: Promise.resolve({ value: SettingsValue.clone(settings) }) }));
  vi.unstubAllGlobals();
});

describe("Library Settings", () => {
  it("edits locally and saves the whole typed group", async () => {
    open();
    const toggle = await screen.findByRole("checkbox", { name: "Include files without saved versions" });
    await waitFor(() => expect(toggle).toBeEnabled());
    await userEvent.click(toggle);
    expect(update).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(update).toHaveBeenCalledExactlyOnceWith({
      value: { value: { oneofKind: "library", library: { ...initial, includeFilesWithoutVersions: false } } },
    });
    expect(await screen.findByText("Library settings saved.")).toBeVisible();
    expect(screen.getByText(/Locations are unaffected/)).toBeInTheDocument();
    const actions = screen.getByRole("button", { name: "Save" }).parentElement;
    expect(Array.from(actions!.children, (action) => action.textContent?.trim())).toEqual(["Save", "Cancel", "Import", "Export"]);
  });

  it("cancels unsaved Trash confirmation changes", async () => {
    open();
    const toggle = await screen.findByRole("checkbox", { name: "Confirm Delete" });
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
    const toggle = await screen.findByRole("checkbox", { name: "Confirm Delete" });
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
    expect(screen.getByRole("progressbar", { name: "Loading Library settings" })).toBeVisible();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Import" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Export" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("keeps Import and Export available after the Settings read fails", async () => {
    get.mockReturnValue({ response: Promise.reject(new Error("Settings unavailable")) });
    open();
    expect(await screen.findByText("Settings unavailable")).toBeVisible();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export" })).toBeEnabled();
    await userEvent.click(screen.getByRole("button", { name: "Import" }));
    expect(screen.getByRole("dialog", { name: "Import Library Backup" })).toBeVisible();
  });

  it("reports Library import scope and preserves global preferences", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetch);
    open();
    await userEvent.click(await screen.findByRole("button", { name: "Import" }));
    expect(
      screen.getByText(
        "Import replaces the Library, including Locations and original links, from this backup. Global preferences stay unchanged. Disk files are not restored.",
      ),
    ).toBeVisible();
    const input = document.querySelector<HTMLInputElement>('input[type="file"]');
    expect(input).not.toBeNull();
    await userEvent.upload(input!, new File(["backup"], "library.jsonl", { type: "application/x-ndjson" }));
    await userEvent.click(await screen.findByRole("button", { name: "Import" }));
    expect(await screen.findByText("Library, Locations and original links imported. Global preferences were not changed.")).toBeVisible();
    expect(fetch).toHaveBeenCalledWith("/files/library/_import", expect.objectContaining({ method: "POST" }));
    expect(list).toHaveBeenCalledExactlyOnceWith({ afterId: 0n, limit: 50, query: "" });
  });

  it.each([false, true])("replaces a pending pre-import catalog and updates mounted choices (cached=%s)", async (cached) => {
    const before = Location.create({ id: 1n, name: "Before import" });
    const after = Location.create({ id: 2n, name: "After import" });
    const store = createAppStore();
    if (cached) {
      list.mockReturnValueOnce({ response: Promise.resolve({ locations: [before], hasMore: false }) });
      await loadLocations(store);
    }
    let finishOld!: (page: { locations: Location[]; hasMore: boolean }) => void;
    list.mockReturnValueOnce({ response: new Promise((resolve) => (finishOld = resolve)) });
    const oldRead = loadLocations(store, true);
    list.mockReturnValueOnce({ response: Promise.resolve({ locations: [after], hasMore: false }) });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("{}", { status: 200 })));
    // Import remains usable even if the unrelated Settings request never completes.
    get.mockReturnValue({ response: new Promise(() => {}) });
    const Choices = () => {
      const { locations } = useLocationChoices({ enabled: true });
      return <output aria-label="Configured choices">{locations.map((location) => location.name).join(",")}</output>;
    };
    render(
      <>
        <SettingsBrowser />
        <Choices />
      </>,
      { store },
    );
    await userEvent.click(screen.getByRole("button", { name: "Import" }));
    await userEvent.upload(
      document.querySelector<HTMLInputElement>('input[type="file"]')!,
      new File(["backup"], "library.jsonl", { type: "application/x-ndjson" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Import" }));
    expect(await screen.findByText("Library, Locations and original links imported. Global preferences were not changed.")).toBeVisible();
    expect(screen.getByLabelText("Configured choices")).toHaveTextContent("After import");
    await act(async () => {
      finishOld({ locations: [before], hasMore: false });
      await oldRead;
    });
    expect(selectLocations(store.getState())).toEqual([after]);
    expect(screen.getByLabelText("Configured choices")).toHaveTextContent("After import");
    expect(list).toHaveBeenCalledTimes(cached ? 3 : 2);
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
    await userEvent.click(await screen.findByRole("button", { name: "Import" }));
    const input = document.querySelector<HTMLInputElement>('input[type="file"]');
    await userEvent.upload(input!, new File(["backup"], "library.jsonl", { type: "application/x-ndjson" }));
    await userEvent.click(await screen.findByRole("button", { name: "Import" }));
    expect(screen.getByRole("progressbar", { name: "Importing Library backup" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    reject(new Error("Import unavailable"));
    expect(await screen.findByText("Import unavailable")).toBeVisible();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled();
    expect(list).not.toHaveBeenCalled();
  });
});

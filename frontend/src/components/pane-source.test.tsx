import { useState } from "react";
import { act, screen, waitFor } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FileBrowser, FileNavbar, FileToolbar, FileList } from "@samuelncui/chonky";
import { Location } from "@/entity";
import { createAppStore } from "@/state/store";
import { loadLocations, saveLocation } from "@/state/locations";
const { list, reportError } = vi.hoisted(() => ({ list: vi.fn(), reportError: vi.fn() }));
vi.mock("@/api", () => ({ locationCli: { list } }));
vi.mock("react-toastify", () => ({ toast: { error: reportError } }));
import { librarySource, PaneSourceSelector, storedPaneSource, type PaneSource } from "./pane-source";

beforeEach(() => {
  vi.resetAllMocks();
  localStorage.clear();
  sessionStorage.clear();
  list.mockReturnValue({ response: Promise.resolve({ locations: [], hasMore: false }) });
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
const Browser = ({
  onRoot = () => {},
  ancestors = ["Trips"],
  onAction = () => {},
}: {
  onRoot?: () => void;
  ancestors?: string[];
  onAction?: (action: unknown) => void;
}) => {
  const [source, setSource] = useState<PaneSource>({ kind: "library" });
  return (
    <FileBrowser
      files={[]}
      onFileAction={onAction}
      folderChain={[{ id: "0", name: "Library", isDir: true }, ...ancestors.map((name, index) => ({ id: String(index + 1), name, isDir: true }))]}
    >
      <FileNavbar rootContent={<PaneSourceSelector source={source} onChange={setSource} onNavigateRoot={onRoot} />} />
      <FileToolbar />
      <FileList />
    </FileBrowser>
  );
};
describe("Shared root selector", () => {
  it("replaces only the root breadcrumb and keeps ancestor navigation and the toolbar", async () => {
    const errors = vi.spyOn(console, "error");
    list.mockReturnValue({
      response: Promise.resolve({
        locations: [Location.create({ id: 4n, name: "Photos" }), Location.create({ id: 5n, name: "Recovery", restoreTarget: true })],
        hasMore: false,
      }),
    });
    render(<Browser />);
    expect(screen.getByRole("button", { name: "Trips" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Choose file source" }));
    expect(await screen.findByRole("menuitem", { name: "Photos" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Recovery" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("menuitem", { name: "Photos" }));
    expect(screen.getByRole("button", { name: "Go to Photos root" })).toHaveTextContent("Photos");
    expect(screen.getByRole("button", { name: "Trips" })).toBeInTheDocument();
    expect(list).toHaveBeenCalledWith({ afterId: 0n, limit: 50, query: "" });
    expect(errors.mock.calls.flat().join(" ")).not.toContain("Breadcrumbs component doesn't accept a Fragment");
  });
  it("offers separate root navigation and folded ancestor navigation", async () => {
    let width = 250;
    let resize = () => {};
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(function (this: HTMLElement) {
      return this.tagName === "NAV" ? width : 100;
    });
    vi.spyOn(HTMLElement.prototype, "scrollWidth", "get").mockReturnValue(100);
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
      width: 100,
      height: 28,
      x: 0,
      y: 0,
      top: 0,
      left: 0,
      bottom: 28,
      right: 100,
      toJSON: () => ({}),
    });
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(callback: () => void) {
          resize = callback;
        }
        observe() {}
        disconnect() {}
        unobserve() {}
      },
    );
    const onRoot = vi.fn();
    const onAction = vi.fn();
    render(<Browser onRoot={onRoot} onAction={onAction} ancestors={["Trips", "Europe", "Alps", "Photos"]} />);
    await userEvent.click(screen.getByRole("button", { name: "Go to Library root" }));
    expect(onRoot).toHaveBeenCalledOnce();
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Europe" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Show parent folders" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Europe" }));
    expect(onAction).toHaveBeenCalledWith(
      expect.objectContaining({ id: "open_files", payload: expect.objectContaining({ targetFile: expect.objectContaining({ id: "2" }) }) }),
    );
    await waitFor(() => expect(screen.queryByRole("menu")).not.toBeInTheDocument());
    await act(async () => {
      width = 700;
      resize();
    });
    expect(screen.getByRole("button", { name: "Europe" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Show parent folders" })).not.toBeInTheDocument();
  });

  it("copies a source-qualified path without changing navigation", async () => {
    const user = userEvent.setup();
    const write = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    const action = vi.fn();
    render(<Browser onAction={action} />);
    await user.click(screen.getByRole("button", { name: "Copy path" }));
    expect(write).toHaveBeenCalledWith("Library/Trips");
    expect(action).not.toHaveBeenCalled();
  });
});

describe("Pane source preferences", () => {
  it("reads only the existing pane key from local storage and leaves persistence with its caller", () => {
    const source: PaneSource = { kind: "location", id: "9007199254740993", name: "Photos" };
    localStorage.setItem("left:source", JSON.stringify(source));
    sessionStorage.setItem("right:source", JSON.stringify(source));
    const write = vi.spyOn(Storage.prototype, "setItem");
    expect(storedPaneSource("left")).toEqual(source);
    expect(storedPaneSource("right")).toEqual(librarySource);
    expect(write).not.toHaveBeenCalled();
  });

  it.each(["{", "null", JSON.stringify({ kind: "location", id: "0", name: "Photos" }), JSON.stringify({ kind: "location", id: "1", name: 3 })])(
    "falls back to Library for invalid storage: %s",
    (raw) => {
      localStorage.setItem("left:source", raw);
      expect(storedPaneSource("left")).toEqual(librarySource);
      expect(localStorage.getItem("left:source")).toBe(raw);
    },
  );

  it("falls back to Library when storage access is denied", () => {
    vi.spyOn(window, "localStorage", "get").mockImplementation(() => {
      throw new Error("Storage denied");
    });
    expect(storedPaneSource("left")).toEqual(librarySource);
  });
});

it("loads before opening, keeps choices immediately on reopening and navigates independently", async () => {
  list.mockReturnValue({ response: Promise.resolve({ locations: [Location.create({ id: 7n, name: "Photos" })], hasMore: false }) });
  const onChange = vi.fn();
  const onNavigateRoot = vi.fn();
  render(<PaneSourceSelector source={librarySource} onChange={onChange} onNavigateRoot={onNavigateRoot} />);
  await act(async () => {});
  expect(list).toHaveBeenCalledOnce();
  await userEvent.click(screen.getByRole("button", { name: "Choose file source" }));
  expect(screen.getByRole("menuitem", { name: "Photos" })).toBeVisible();
  expect(screen.queryByText("Loading Locations…")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("menuitem", { name: "Photos" }));
  expect(onChange).toHaveBeenCalledWith({ kind: "location", id: "7", name: "Photos" });
  await userEvent.click(screen.getByRole("button", { name: "Choose file source" }));
  expect(screen.getByRole("menuitem", { name: "Photos" })).toBeVisible();
  expect(screen.queryByText("Loading Locations…")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("menuitem", { name: "Library" }));
  await userEvent.click(screen.getByRole("button", { name: "Go to Library root" }));
  expect(onNavigateRoot).toHaveBeenCalledOnce();
  expect(list).toHaveBeenCalledOnce();
});

it("distinguishes initial loading, failed reads and a confirmed empty catalog", async () => {
  let reject!: (reason: Error) => void;
  list.mockReturnValueOnce({
    response: new Promise((_, fail) => {
      reject = fail;
    }),
  });
  render(<PaneSourceSelector source={librarySource} onChange={vi.fn()} onNavigateRoot={vi.fn()} />);
  await userEvent.click(screen.getByRole("button", { name: "Choose file source" }));
  expect(screen.getByText("Loading Locations…")).toBeVisible();
  expect(screen.queryByText("No locations")).not.toBeInTheDocument();
  await act(async () => reject(new Error("Offline")));
  expect(screen.getByText("Offline")).toBeVisible();
  expect(screen.queryByText("No locations")).not.toBeInTheDocument();
  list.mockReturnValueOnce({ response: Promise.resolve({ locations: [], hasMore: false }) });
  await userEvent.click(screen.getByRole("menuitem", { name: "Retry locations" }));
  await waitFor(() => expect(screen.getByText("No locations")).toBeVisible());
  expect(screen.queryByText("Offline")).not.toBeInTheDocument();
});

it("reflects a saved Location name in the selected root and menu without another request", async () => {
  const source = Location.create({ id: 7n, name: "Photos" });
  list.mockReturnValue({ response: Promise.resolve({ locations: [source], hasMore: false }) });
  const store = createAppStore();
  await loadLocations(store);
  render(<PaneSourceSelector source={{ kind: "location", id: "7", name: "Photos" }} onChange={vi.fn()} onNavigateRoot={vi.fn()} />, { store });
  act(() => saveLocation(store, Location.create({ ...source, name: "Pictures" })));
  expect(screen.getByRole("button", { name: "Go to Pictures root" })).toBeVisible();
  await userEvent.click(screen.getByRole("button", { name: "Choose file source" }));
  expect(screen.getByRole("menuitem", { name: "Pictures" })).toHaveClass("Mui-selected");
  expect(list).toHaveBeenCalledOnce();
});

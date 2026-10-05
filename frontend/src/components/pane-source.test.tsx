import { useState } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FileBrowser, FileNavbar, FileToolbar, FileList } from "@samuelncui/chonky";
import { Location } from "@/entity";
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
    expect(list).toHaveBeenCalledWith({ afterId: 0n, limit: 50, query: "" }, expect.objectContaining({ abort: expect.any(AbortSignal) }));
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

describe("Location menu requests", () => {
  const show = () => render(<PaneSourceSelector source={librarySource} onChange={vi.fn()} onNavigateRoot={vi.fn()} />);
  const open = async () => userEvent.click(screen.getByRole("button", { name: "Choose file source" }));
  const close = async () => {
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("menu")).not.toBeInTheDocument());
  };

  it.each([false, true])("ignores a closed opening's reply after reopening (failure: %s)", async (failed) => {
    let complete!: (reply: unknown) => void;
    let reject!: (error: Error) => void;
    list.mockReturnValueOnce({
      response: new Promise((resolve, fail) => {
        complete = resolve;
        reject = fail;
      }),
    });
    const view = show();
    expect(list).not.toHaveBeenCalled();
    await open();
    const signal = list.mock.calls[0][1].abort as AbortSignal;
    await close();
    expect(signal.aborted).toBe(true);
    list.mockReturnValueOnce({ response: Promise.resolve({ locations: [Location.create({ id: 9n, name: "Current" })], hasMore: false }) });
    await open();
    await screen.findByRole("menuitem", { name: "Current" });
    await act(async () => {
      if (failed) reject(new Error("Obsolete failure"));
      else complete({ locations: [Location.create({ id: 1n, name: "Obsolete" })], hasMore: true });
    });
    expect(screen.queryByRole("menuitem", { name: "Obsolete" })).not.toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Load more…" })).not.toBeInTheDocument();
    expect(reportError).not.toHaveBeenCalled();
    expect(list).toHaveBeenCalledTimes(2);
    view.unmount();
  });

  it("serializes load-more clicks and deduplicates overlapping Location IDs", async () => {
    const first = Location.create({ id: 1n, name: "First" });
    list.mockReturnValueOnce({ response: Promise.resolve({ locations: [first], hasMore: true }) });
    show();
    await open();
    const more = await screen.findByRole("menuitem", { name: "Load more…" });
    let complete!: (reply: unknown) => void;
    list.mockReturnValueOnce({
      response: new Promise((resolve) => {
        complete = resolve;
      }),
    });
    await userEvent.dblClick(more);
    expect(list).toHaveBeenCalledTimes(2);
    expect(list.mock.calls[1][0]).toEqual({ afterId: 1n, limit: 50, query: "" });
    expect(more).toHaveAttribute("aria-disabled", "true");
    await act(async () => complete({ locations: [first, Location.create({ id: 2n, name: "Second" })], hasMore: false }));
    expect(screen.getAllByRole("menuitem", { name: "First" })).toHaveLength(1);
    expect(screen.getByRole("menuitem", { name: "Second" })).toBeInTheDocument();
  });

  it("does not let a late page settle or append to a reopened menu", async () => {
    list.mockReturnValueOnce({ response: Promise.resolve({ locations: [Location.create({ id: 1n, name: "First" })], hasMore: true }) });
    show();
    await open();
    let completePage!: (reply: unknown) => void;
    list.mockReturnValueOnce({
      response: new Promise((resolve) => {
        completePage = resolve;
      }),
    });
    await userEvent.click(await screen.findByRole("menuitem", { name: "Load more…" }));
    const signal = list.mock.calls[1][1].abort as AbortSignal;
    await close();
    expect(signal.aborted).toBe(true);
    let completeOpening!: (reply: unknown) => void;
    list.mockReturnValueOnce({
      response: new Promise((resolve) => {
        completeOpening = resolve;
      }),
    });
    await open();
    await act(async () => completePage({ locations: [Location.create({ id: 2n, name: "Stale page" })], hasMore: true }));
    expect(screen.getByRole("menuitem", { name: "Loading Locations…" })).toHaveAttribute("aria-disabled", "true");
    expect(screen.queryByRole("menuitem", { name: "Load more…" })).not.toBeInTheDocument();
    expect(list).toHaveBeenCalledTimes(3);
    expect(list.mock.calls[2][0]).toEqual({ afterId: 0n, limit: 50, query: "" });
    expect(screen.queryByRole("menuitem", { name: "Stale page" })).not.toBeInTheDocument();
    await act(async () => completeOpening({ locations: [Location.create({ id: 9n, name: "Current" })], hasMore: false }));
    expect(screen.getByRole("menuitem", { name: "Current" })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "First" })).not.toBeInTheDocument();
  });

  it("keeps loaded Locations and retries a failed page inline", async () => {
    list.mockReturnValueOnce({ response: Promise.resolve({ locations: [Location.create({ id: 1n, name: "First" })], hasMore: true }) });
    show();
    await open();
    list.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Locations offline")) }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Load more…" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Locations offline");
    expect(reportError).not.toHaveBeenCalled();
    expect(screen.getByRole("menuitem", { name: "First" })).toBeInTheDocument();
    list.mockReturnValueOnce({ response: Promise.resolve({ locations: [Location.create({ id: 2n, name: "Second" })], hasMore: false }) });
    await userEvent.click(screen.getByRole("menuitem", { name: "Retry locations" }));
    await screen.findByRole("menuitem", { name: "Second" });
    expect(list.mock.calls[2][0]).toEqual({ afterId: 1n, limit: 50, query: "" });
  });

  it("keeps Library navigation available and retries an initial Location failure", async () => {
    list.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Locations offline")) }));
    show();
    await open();
    await screen.findByRole("alert");
    expect(screen.getByRole("menuitem", { name: "Library" })).not.toHaveAttribute("aria-disabled", "true");
    expect(screen.queryByRole("menuitem", { name: "No locations" })).not.toBeInTheDocument();
    list.mockReturnValueOnce({ response: Promise.resolve({ locations: [Location.create({ id: 2n, name: "Available" })], hasMore: false }) });
    await userEvent.keyboard("{End}");
    expect(screen.getByRole("menuitem", { name: "Retry locations" })).toHaveFocus();
    await userEvent.keyboard("{Enter}");
    await screen.findByRole("menuitem", { name: "Available" });
    expect(list.mock.calls[1][0]).toEqual({ afterId: 0n, limit: 50, query: "" });
  });

  it.each(["selection", "unmount"] as const)("cancels pending menu work on %s", async (action) => {
    let reject!: (error: Error) => void;
    list.mockReturnValueOnce({
      response: new Promise((_, fail) => {
        reject = fail;
      }),
    });
    const view = show();
    await open();
    const signal = list.mock.calls[0][1].abort as AbortSignal;
    if (action === "selection") await userEvent.click(screen.getByRole("menuitem", { name: "Library" }));
    else view.unmount();
    expect(signal.aborted).toBe(true);
    await act(async () => reject(new Error("Cancelled read failed")));
    expect(reportError).not.toHaveBeenCalled();
    expect(list).toHaveBeenCalledOnce();
  });
});

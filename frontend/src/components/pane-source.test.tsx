import { useState } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { FileBrowser, FileNavbar, FileToolbar, FileList } from "@samuelncui/chonky";
import { Location } from "@/entity";
const { list } = vi.hoisted(() => ({ list: vi.fn() }));
vi.mock("@/api", () => ({ locationCli: { list } }));
import { PaneSourceSelector, type PaneSource } from "./pane-source";
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
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });
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

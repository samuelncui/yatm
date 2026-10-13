import { type ReactNode } from "react";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { ChonkyActions, ChonkyIconName, FileList, FileToolbar, type ChonkyIconProps } from "@samuelncui/chonky";
import { render } from "@/state/test-render";
import { createAppStore } from "@/state/store";
import { fileListStorageKey } from "@/state/file-lists";
import { FileBrowser } from "./file-browser";

vi.mock("react-virtuoso", () => ({
  Virtuoso: ({ totalCount, itemContent }: { totalCount: number; itemContent: (index: number) => ReactNode }) => (
    <div>
      {Array.from({ length: totalCount }, (_, index) => (
        <div key={index}>{itemContent(index)}</div>
      ))}
    </div>
  ),
}));
const files = [
  { id: "a", name: "a.txt", size: 1 },
  { id: "b", name: "b.txt", size: 3 },
  { id: "z", name: "z", isDir: true, size: 2 },
];
const partialActions = [
  ChonkyActions.SortFilesByName.id,
  ChonkyActions.SortFilesBySize.id,
  ChonkyActions.SortFilesByDate.id,
  ChonkyActions.ToggleShowFoldersFirst.id,
];
const Icon = ({ icon }: ChonkyIconProps) => <span data-testid={`icon-${icon}`} />;
const Browser = ({ partial = false, path = "root", id = "left" }: { partial?: boolean; path?: string; id?: string }) => (
  <FileBrowser
    instanceId={id}
    files={files}
    folderChain={[{ id: path, name: path, isDir: true }]}
    disableDragAndDrop
    disableDefaultFileActions={partial ? partialActions : undefined}
    iconComponent={Icon}
  >
    <FileToolbar />
    <FileList />
  </FileBrowser>
);
const order = () => [...screen.getByRole("list").querySelectorAll("[data-chonky-file-id]")].map((element) => element.getAttribute("data-chonky-file-id"));
const options = () => userEvent.click(screen.getByRole("button", { name: "Options" }));
const choose = async (name: string) => {
  await options();
  await userEvent.click(screen.getByRole("menuitem", { name }));
};
beforeEach(() => localStorage.clear());

it("initializes actual sort and menu together after a partial listing completes", async () => {
  const view = render(<Browser partial />);
  await waitFor(() => expect(order()).toEqual(["a", "b", "z"]));
  view.rerender(<Browser />);
  await waitFor(() => expect(order()).toEqual(["z", "a", "b"]));
  await options();
  expect(within(screen.getByRole("menuitem", { name: "Sort by name" })).getByTestId(`icon-${ChonkyIconName.sortAsc}`)).toBeInTheDocument();
  expect(within(screen.getByRole("menuitem", { name: "Show folders first" })).getByTestId(`icon-${ChonkyIconName.toggleOn}`)).toBeInTheDocument();
});

it("keeps choice, direction and folders-first per list across navigation, partial reads and remounts", async () => {
  const first = createAppStore();
  const view = render(<Browser />, { store: first });
  await waitFor(() => expect(order()).toEqual(["z", "a", "b"]));
  await choose("Show folders first");
  await choose("Sort by size");
  await waitFor(() => expect(order()).toEqual(["b", "z", "a"]));
  await choose("Sort by size");
  await waitFor(() => expect(order()).toEqual(["a", "z", "b"]));
  view.rerender(<Browser partial path="other" />);
  await waitFor(() => expect(order()).toEqual(["a", "b", "z"]));
  view.rerender(<Browser path="other" />);
  await waitFor(() => expect(order()).toEqual(["a", "z", "b"]));
  expect(first.getState().fileLists.left).toEqual({ sort: "size", descending: false, foldersFirst: false });
  view.unmount();
  const restored = render(<Browser path="third" />);
  await waitFor(() => expect(order()).toEqual(["a", "z", "b"]));
  await options();
  expect(within(screen.getByRole("menuitem", { name: "Sort by size" })).getByTestId(`icon-${ChonkyIconName.sortAsc}`)).toBeInTheDocument();
  expect(within(screen.getByRole("menuitem", { name: "Show folders first" })).getByTestId(`icon-${ChonkyIconName.toggleOff}`)).toBeInTheDocument();
  restored.unmount();
  render(<Browser id="right" />);
  await waitFor(() => expect(order()).toEqual(["z", "a", "b"]));
});

it("uses defaults for invalid saved data and keeps preference changes when storage fails", async () => {
  localStorage.setItem(fileListStorageKey, '{"left":{"sort":"invalid"}}');
  const store = createAppStore();
  render(<Browser />, { store });
  await waitFor(() => expect(order()).toEqual(["z", "a", "b"]));
  const write = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new Error("storage denied");
  });
  await choose("Sort by size");
  await waitFor(() => expect(order()).toEqual(["z", "b", "a"]));
  expect(store.getState().fileLists.left.sort).toBe("size");
  write.mockRestore();
});

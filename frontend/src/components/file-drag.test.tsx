import type { ReactNode } from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { ChonkyActions, FileBrowser, FileList } from "@samuelncui/chonky";
import { expect, it, vi } from "vitest";

vi.mock("react-virtuoso", () => ({
  Virtuoso: ({ totalCount, itemContent }: { totalCount: number; itemContent: (index: number) => ReactNode }) => (
    <div>
      {Array.from({ length: totalCount }, (_, index) => (
        <div key={index}>{itemContent(index)}</div>
      ))}
    </div>
  ),
}));

it.each(["background", "folder"])("keeps drag connectors and one preview stable over the other pane's %s, then dispatches one move", async (target) => {
  const action = vi.fn();
  const files = [{ id: "file", name: "drag-me.txt" }];
  render(
    <>
      <section aria-label="Source">
        <FileBrowser files={files} folderChain={[{ id: "source", name: "Source", isDir: true }]} onFileAction={action}>
          <FileList />
        </FileBrowser>
      </section>
      <section aria-label="Destination">
        <FileBrowser
          files={[{ id: "folder", name: "Folder", isDir: true }]}
          folderChain={[{ id: "destination", name: "Destination", isDir: true }]}
          onFileAction={action}
        >
          <FileList />
        </FileBrowser>
      </section>
    </>,
  );
  const source = await within(screen.getByRole("region", { name: "Source" })).findByRole("listitem");
  const destination = within(screen.getByRole("region", { name: "Destination" })).getByRole(target === "folder" ? "listitem" : "list");
  const removeListener = vi.spyOn(EventTarget.prototype, "removeEventListener");
  const dataTransfer = { setData: vi.fn(), setDragImage: vi.fn(), effectAllowed: "all", dropEffect: "move", types: [], files: [] };
  fireEvent.dragStart(source, { clientX: 10, clientY: 10, dataTransfer });
  await waitFor(() => expect(action).toHaveBeenCalledWith(expect.objectContaining({ id: ChonkyActions.StartDragNDrop.id })));
  fireEvent.dragEnter(destination, { clientX: 100, clientY: 10, dataTransfer });
  fireEvent.dragOver(destination, { clientX: 100, clientY: 10, dataTransfer });
  await act(async () => new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve())));
  const disconnected = removeListener.mock.calls.filter(([name]) => ["dragstart", "dragenter", "dragover", "drop"].includes(name));
  const previews = Array.from(document.querySelectorAll("b")).filter((element) => element.textContent === "drag-me.txt");
  fireEvent.drop(destination, { clientX: 100, clientY: 10, dataTransfer });
  fireEvent.dragEnd(source, { dataTransfer });
  removeListener.mockRestore();
  await waitFor(() => expect(action.mock.calls.filter(([data]) => data.id === ChonkyActions.MoveFiles.id)).toHaveLength(1));
  expect(action).toHaveBeenCalledWith(
    expect.objectContaining({
      id: ChonkyActions.MoveFiles.id,
      payload: expect.objectContaining({ destination: expect.objectContaining({ id: target === "folder" ? "folder" : "destination" }), files }),
    }),
  );
  expect(disconnected).toEqual([]);
  expect(previews).toHaveLength(1);
  expect(document.querySelector("b")).not.toBeInTheDocument();
});

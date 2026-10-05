import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { EntryKind, FilesEntry, FileScope, FileSelection, SelectionInspectionResult } from "@/entity";
import { filesEntryData } from "@/components/files-browser";
import { useWaitlistDirectory } from "./waitlist-directory";
const { filesPage, inspect } = vi.hoisted(() => ({ filesPage: vi.fn(), inspect: vi.fn() }));
vi.mock("@/components/files-browser", async (original) => ({ ...(await original<typeof import("@/components/files-browser")>()), filesPage }));
vi.mock("@/api", async (original) => ({
  ...(await original<typeof import("@/api")>()),
  restoreJobCli: { estimate: (...args: unknown[]) => ({ response: inspect(...args).response.then((result: unknown) => ({ result })) }) },
}));
const folder = (id: bigint) => ({
  key: String(id),
  name: `Folder ${id}`,
  path: `Folder ${id}`,
  isDir: true,
  selection: FileSelection.create({ target: { oneofKind: "library", library: { fileId: id } }, scope: FileScope.SAVED }),
});
const page = (id: string, nextCursor = "") => ({ files: [{ id, name: `file-${id}.txt` }], scope: FileScope.SAVED, nextCursor });
beforeEach(() => {
  filesPage.mockReset();
  inspect.mockReset();
  inspect.mockReturnValue({ response: Promise.resolve(SelectionInspectionResult.create()) });
});
describe("waitlist directory reads", () => {
  it.each([false, true])("preserves good siblings and display-only error rows (restore: %s)", async (restore) => {
    const directory = {
      key: "location-folder",
      name: "photos",
      path: "Documents/photos",
      isDir: true,
      selection: FileSelection.create({ target: { oneofKind: "location", location: { locationId: 3n, path: "photos" } }, scope: FileScope.ALL }),
    };
    const good = [8n, 9n].map((id) =>
      FilesEntry.create({
        name: `file-${id}.txt`,
        path: `photos/file-${id}.txt`,
        kind: EntryKind.FILE,
        associatedFileId: id,
        reference: { target: { oneofKind: "location", location: { locationId: 3n, path: `photos/file-${id}.txt` } } },
      }),
    );
    const failed = [
      FilesEntry.create({ name: "bad\\xff", path: "photos/bad\\xff", error: "Unsupported filename: invalid UTF-8" }),
      FilesEntry.create({ name: "bad\\xff", path: "photos/bad\\xff", kind: EntryKind.DIRECTORY, error: "Could not read entry" }),
    ];
    const files = [good[0], ...failed, good[1]].map(filesEntryData);
    filesPage.mockResolvedValue({ files, total: 4n, scope: FileScope.ALL, nextCursor: "" });
    const { result } = renderHook(() => useWaitlistDirectory(directory, restore));
    await waitFor(() => expect(result.current.entries).toHaveLength(4));
    expect(result.current.loading).toBe(false);
    expect(result.current.error).toBe("");
    expect(result.current.entries.map((entry) => entry.name)).toEqual(files.map((file) => file.name));
    expect(new Set(result.current.entries.map((entry) => entry.key)).size).toBe(4);
    for (const [index, failedEntry] of failed.entries()) {
      const entry = result.current.entries[index + 1];
      expect(entry).toMatchObject({
        key: files[index + 1].id,
        name: failedEntry.name,
        path: `Documents/photos/${failedEntry.name}`,
        isDir: failedEntry.kind === EntryKind.DIRECTORY,
        unavailableReason: failedEntry.error,
      });
      expect(entry.selection).toBeUndefined();
      expect(entry.fileID).toBeUndefined();
      expect(entry.version).toBeUndefined();
    }
    expect(result.current.entries[0].selection).toEqual(files[0].originSelection);
    expect(result.current.entries[3].selection).toEqual(files[3].originSelection);
    expect(result.current.entries.map((entry) => entry.fileID)).toEqual(["8", undefined, undefined, "9"]);
    if (restore) {
      await waitFor(() => expect(inspect).toHaveBeenCalledTimes(1));
      expect(inspect.mock.calls[0][0].selections).toEqual(
        [8n, 9n].map((fileId) => FileSelection.create({ target: { oneofKind: "library", library: { fileId } }, scope: FileScope.ALL })),
      );
    } else expect(inspect).not.toHaveBeenCalled();
  });

  it("loads only one child page at a time, preserves scope and resolves only loaded files", async () => {
    filesPage.mockResolvedValueOnce(page("8", "page-2")).mockResolvedValueOnce(page("9"));
    const { result } = renderHook(() => useWaitlistDirectory(folder(1n), true, 1700000000000000123n));
    await waitFor(() => expect(result.current.entries).toHaveLength(1));
    expect(filesPage).toHaveBeenCalledTimes(1);
    expect(filesPage).toHaveBeenCalledWith(expect.objectContaining({ target: { oneofKind: "fileId", fileId: 1n } }), FileScope.SAVED, "");
    expect(inspect).toHaveBeenCalledWith(
      expect.objectContaining({
        versionPolicy: { beforeAtNs: 1700000000000000123n },
        selections: [expect.objectContaining({ target: { oneofKind: "library", library: { fileId: 8n } } })],
      }),
    );
    act(() => {
      result.current.loadMore();
      result.current.loadMore();
    });
    await waitFor(() => expect(result.current.entries).toHaveLength(2));
    expect(filesPage).toHaveBeenCalledTimes(2);
    expect(result.current.entries.map((entry) => entry.fileID)).toEqual(["8", "9"]);
  });
  it("rejects late pages after directory navigation", async () => {
    let finish!: (value: ReturnType<typeof page>) => void;
    filesPage
      .mockReturnValueOnce(
        new Promise((resolve) => {
          finish = resolve;
        }),
      )
      .mockResolvedValueOnce(page("9"));
    const { result, rerender } = renderHook(({ id }) => useWaitlistDirectory(folder(id), false), { initialProps: { id: 1n } });
    rerender({ id: 2n });
    await waitFor(() => expect(result.current.entries[0]?.fileID).toBe("9"));
    await act(async () => finish(page("8")));
    expect(result.current.entries[0]?.fileID).toBe("9");
    expect(inspect).not.toHaveBeenCalled();
  });
  it("clears rows on a failed child page and retries from the beginning", async () => {
    filesPage.mockResolvedValueOnce(page("8", "page-2")).mockRejectedValueOnce(new Error("Folder unavailable")).mockResolvedValueOnce(page("9"));
    const { result } = renderHook(() => useWaitlistDirectory(folder(1n), false));
    await waitFor(() => expect(result.current.entries).toHaveLength(1));
    act(() => result.current.loadMore());
    await waitFor(() => expect(result.current.error).toContain("Folder unavailable"));
    expect(result.current.entries).toEqual([]);
    act(() => result.current.retry());
    await waitFor(() => expect(result.current.entries[0]?.fileID).toBe("9"));
    expect(filesPage.mock.calls.at(-1)?.[2]).toBe("");
  });
  it("uses live Location directory references without admitting files", async () => {
    filesPage.mockResolvedValue({ files: [], scope: FileScope.ALL, nextCursor: "" });
    const directory = {
      key: "location-folder",
      name: "photos",
      path: "Documents/photos",
      isDir: true,
      selection: FileSelection.create({
        target: {
          oneofKind: "location",
          location: { locationId: 3n, path: "photos" },
        },
        scope: FileScope.ALL,
      }),
    };
    const { result } = renderHook(() => useWaitlistDirectory(directory, false));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(filesPage).toHaveBeenCalledWith(
      expect.objectContaining({
        target: { oneofKind: "location", location: expect.objectContaining({ locationId: 3n, path: "photos" }) },
      }),
      FileScope.ALL,
      "",
    );
    expect(inspect).not.toHaveBeenCalled();
  });
});

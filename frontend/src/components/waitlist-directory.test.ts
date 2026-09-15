import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { FileScope, FileSelection, InspectSelectionReply } from "@/entity";
import { useWaitlistDirectory } from "./waitlist-directory";
const { filesPage, inspect } = vi.hoisted(() => ({ filesPage: vi.fn(), inspect: vi.fn() }));
vi.mock("@/components/files-browser", async (original) => ({ ...(await original<typeof import("@/components/files-browser")>()), filesPage }));
vi.mock("@/api", async (original) => ({ ...(await original<typeof import("@/api")>()), fileCatalogCli: { inspectSelection: inspect } }));
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
  inspect.mockReturnValue({ response: Promise.resolve(InspectSelectionReply.create()) });
});
describe("waitlist directory reads", () => {
  it("loads only one child page at a time, preserves scope and resolves only loaded files", async () => {
    filesPage.mockResolvedValueOnce(page("8", "page-2")).mockResolvedValueOnce(page("9"));
    const { result } = renderHook(() => useWaitlistDirectory(folder(1n), true, 1000n));
    await waitFor(() => expect(result.current.entries).toHaveLength(1));
    expect(filesPage).toHaveBeenCalledTimes(1);
    expect(filesPage).toHaveBeenCalledWith(expect.objectContaining({ target: { oneofKind: "fileId", fileId: 1n } }), FileScope.SAVED, "");
    expect(inspect).toHaveBeenCalledWith(
      expect.objectContaining({
        versionPolicy: { beforeAtMs: 1000n },
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
          location: { locationId: 3n, path: "photos", revision: 0n, reference: { locationId: 3n, path: "photos", bindingToken: "binding" } },
        },
        scope: FileScope.ALL,
      }),
    };
    const { result } = renderHook(() => useWaitlistDirectory(directory, false));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(filesPage).toHaveBeenCalledWith(
      expect.objectContaining({
        target: { oneofKind: "location", location: expect.objectContaining({ locationId: 3n, path: "photos", bindingToken: "binding" }) },
      }),
      FileScope.ALL,
      "",
    );
    expect(inspect).not.toHaveBeenCalled();
  });
});

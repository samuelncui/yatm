import { createRef, useEffect, type PropsWithChildren, type ReactNode } from "react";
import { act, cleanup, renderHook, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ChonkyActions, FileBrowser, FileList, type FileArray, type FileBrowserHandle } from "@samuelncui/chonky";
import { AppStateProvider } from "@/state/react";
import { render } from "@/state/test-render";
import { EntryKind, FileScope, FilesEntry, ListFilesResponse } from "@/entity";
import { useFileBrowser } from "./file";

const { list, search, toastError } = vi.hoisted(() => ({ list: vi.fn(), search: vi.fn(), toastError: vi.fn() }));
vi.mock("@/api", async (original) => ({
  ...(await original<typeof import("@/api")>()),
  filesCli: { list, search },
}));
vi.mock("react-toastify", () => ({ toast: { error: toastError } }));
// Keep Chonky's actual reducers, selectors, sorting and rows; only jsdom geometry is absent.
vi.mock("react-virtuoso", () => ({
  Virtuoso: ({ totalCount, itemContent }: { totalCount: number; itemContent: (index: number) => ReactNode }) => (
    <div>
      {Array.from({ length: totalCount }, (_, index) => (
        <div key={index}>{itemContent(index)}</div>
      ))}
    </div>
  ),
}));

const wrapper = ({ children }: PropsWithChildren) => (
  <AppStateProvider>
    <MemoryRouter>{children}</MemoryRouter>
  </AppStateProvider>
);
const noop = () => {};
const refreshAll = async () => {};
const rows = (offset: number, count: number) =>
  Array.from({ length: count }, (_, index) =>
    FilesEntry.create({
      reference: { target: { oneofKind: "fileId", fileId: BigInt(offset + index + 1) } },
      name: `file-${offset + index + 1}`,
      kind: EntryKind.FILE,
      sizeBytes: BigInt(offset + index + 1),
    }),
  );

// Each send settles one awaited transport read. Frames are driven separately, so tests
// exercise both burst delivery and a slow stream without relying on wall-clock timings.
function stream() {
  let resolve!: (result: IteratorResult<ListFilesResponse>) => void;
  let reject!: (error: Error) => void;
  const next = vi.fn(
    () =>
      new Promise<IteratorResult<ListFilesResponse>>((done, fail) => {
        resolve = done;
        reject = fail;
      }),
  );
  return {
    responses: { [Symbol.asyncIterator]: () => ({ next }) },
    send: (entries: FilesEntry[], total?: bigint) =>
      act(async () => {
        resolve({ done: false, value: ListFilesResponse.create({ entries, totalEntryCount: total, scope: FileScope.ALL }) });
      }),
    finish: () =>
      act(async () => {
        resolve({ done: true, value: undefined });
      }),
    fail: () =>
      act(async () => {
        reject(new Error("Read failed"));
      }),
  };
}

let frames: Map<number, FrameRequestCallback>;
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  list.mockReset();
  search.mockReset().mockReturnValue({ response: Promise.resolve({ entries: [], nextCursor: "", breadcrumbs: [], scope: FileScope.ALL }) });
  toastError.mockReset();
  frames = new Map();
  let id = 0;
  vi.stubGlobal(
    "requestAnimationFrame",
    vi.fn((callback: FrameRequestCallback) => {
      frames.set(++id, callback);
      return id;
    }),
  );
  vi.stubGlobal(
    "cancelAnimationFrame",
    vi.fn((id: number) => frames.delete(id)),
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
const paint = () =>
  act(async () => {
    const pending = [...frames.values()];
    frames.clear();
    for (const callback of pending) callback(0);
  });
function mount() {
  const handle = createRef<FileBrowserHandle>();
  const snapshots: FileArray[] = [];
  return {
    snapshots,
    ...renderHook(
      ({ enabled, scope }) => {
        const browser = useFileBrowser(handle, "stream", refreshAll, undefined, noop, undefined, undefined, scope, undefined, undefined, enabled);
        useEffect(() => {
          if (browser.files.length) snapshots.push(browser.files);
        }, [browser.files]);
        return browser;
      },
      { wrapper, initialProps: { enabled: true, scope: FileScope.ALL } },
    ),
  };
}

it.each([10_000, 100_001])("bounds supplied prefixes below 3N across a %i-row slow stream", async (total) => {
  const reading = stream();
  list.mockReturnValue(reading);
  const { result, snapshots } = mount();
  const disabled = new Set([result.current.browserProps.disableDefaultFileActions]);
  for (let offset = 0; offset < total; offset += 500) {
    await reading.send(rows(offset, Math.min(500, total - offset)), offset === 0 ? BigInt(total) : undefined);
    expect(result.current.total).toBe(BigInt(total));
    expect(result.current.listProps.reading).toBe(true);
    disabled.add(result.current.browserProps.disableDefaultFileActions);
    await paint();
  }
  expect(snapshots[0]).toHaveLength(500);
  expect(snapshots.length).toBeGreaterThan(2);
  await reading.finish();
  expect(result.current.files).toHaveLength(total);
  expect(result.current.files.at(-1)?.name).toBe(`file-${total}`);
  expect(result.current.listProps.reading).toBe(false);
  expect(result.current.browserProps.disableDefaultFileActions).toBeUndefined();
  expect(snapshots.reduce((cost, files) => cost + files.length, 0)).toBeLessThan(3 * total);
  expect(disabled.size).toBe(1);
  expect(frames.size).toBe(0);
  expect(list).toHaveBeenCalledOnce();
});

it.each([4, 8])("shows the first batch immediately, coalesces bursts and finishes with %i remaining rows", async (remaining) => {
  const reading = stream();
  list.mockReturnValue(reading);
  const { result } = mount();
  await reading.send(rows(0, 2), BigInt(6 + remaining));
  const first = result.current.files;
  expect(first).toHaveLength(2);
  expect(frames.size).toBe(0);
  await reading.send(rows(2, 2));
  await reading.send(rows(4, 2));
  expect(frames.size).toBe(1);
  expect(result.current.files).toBe(first);
  await paint();
  expect(result.current.files).toHaveLength(6);
  expect(first).toHaveLength(2);
  await reading.send(rows(6, remaining));
  expect(result.current.files).toHaveLength(6);
  await reading.finish();
  expect(result.current.files).toHaveLength(6 + remaining);
  expect(result.current.total).toBe(BigInt(6 + remaining));
  expect(frames.size).toBe(0);
  await paint();
  expect(result.current.files).toHaveLength(6 + remaining);
});

it("settles an empty directory with its exact zero total", async () => {
  const reading = stream();
  list.mockReturnValue(reading);
  const { result } = mount();
  await reading.send([], 0n);
  expect(result.current.total).toBe(0n);
  expect(result.current.listProps.reading).toBe(true);
  await reading.finish();
  expect(result.current.files).toEqual([]);
  expect(result.current.total).toBe(0n);
  expect(result.current.listProps.reading).toBe(false);
  expect(frames.size).toBe(0);
});

it.each(["library", "location"])("discards a queued frame on a failed %s refresh", async (source) => {
  if (source === "location") localStorage.setItem("stream:source", JSON.stringify({ kind: "location", id: "4", name: "Photos" }));
  const initial = stream();
  list.mockReturnValue(initial);
  const { result } = mount();
  await initial.send(rows(0, 3), 3n);
  await initial.finish();
  const reading = stream();
  list.mockReturnValue(reading);
  let failed!: Promise<unknown>;
  await act(async () => {
    failed = result.current.refresh().catch((error: Error) => error.message);
  });
  await reading.send(rows(10, 2), 4n);
  await reading.send(rows(12, 2));
  expect(frames.size).toBe(1);
  await reading.fail();
  expect(await failed).toBe("Read failed");
  expect(frames.size).toBe(0);
  await paint();
  expect(result.current.files.map((file) => file?.id)).toEqual(source === "library" ? ["1", "2", "3"] : []);
  expect(result.current.total).toBe(source === "library" ? 3n : undefined);
  expect(result.current.loadError).toBe("Read failed");
});

it.each(["search", "refresh", "source", "scope", "hidden", "unmount"])("cancels pending publication and transport on %s", async (action) => {
  const reading = stream();
  list.mockReturnValue(reading);
  const view = mount();
  await reading.send(rows(0, 2), 4n);
  await reading.send(rows(2, 2));
  const lateFrame = [...frames.values()][0];
  expect(lateFrame).toBeDefined();
  const signal = list.mock.calls[0][1].abort as AbortSignal;
  list.mockReturnValue(stream());
  await act(async () => {
    if (action === "search") await view.result.current.search("replacement");
    if (action === "refresh") void view.result.current.refresh();
    if (action === "source") view.result.current.selector.props.onChange({ kind: "location", id: "5", name: "Other" });
    if (action === "scope") view.rerender({ enabled: true, scope: FileScope.SAVED });
    if (action === "hidden") view.rerender({ enabled: false, scope: FileScope.ALL });
    if (action === "unmount") view.unmount();
  });
  expect(signal.aborted).toBe(true);
  expect(frames.size).toBe(0);
  const current = view.result.current.files;
  await act(async () => lateFrame(0));
  await reading.fail();
  expect(view.result.current.files).toBe(current);
  expect(toastError).not.toHaveBeenCalled();
});

it("skips Chonky sorting during streaming and restores the user's sort direction on completion", async () => {
  const reading = stream();
  list.mockReturnValue(reading);
  const handle = createRef<FileBrowserHandle>();
  let browser!: ReturnType<typeof useFileBrowser>;
  function Harness() {
    browser = useFileBrowser(handle, "stream", refreshAll, undefined, noop);
    return (
      <FileBrowser ref={handle} {...browser.browserProps} disableDragAndDrop>
        <FileList {...browser.listProps} />
      </FileBrowser>
    );
  }
  render(
    <MemoryRouter>
      <Harness />
    </MemoryRouter>,
  );
  const sortedSizes: number[] = [];
  const sort = Array.prototype.sort;
  vi.spyOn(Array.prototype, "sort").mockImplementation(function (this: FileArray, compare) {
    if (this[0]?.operationReference) sortedSizes.push(this.length);
    return sort.call(this, compare);
  });
  await reading.send(rows(0, 2), 4n);
  await reading.send(rows(2, 2));
  await paint();
  expect(sortedSizes).toEqual([]);
  await reading.finish();
  await act(async () => {
    await handle.current!.requestFileAction(ChonkyActions.SortFilesBySize, undefined);
    await handle.current!.requestFileAction(ChonkyActions.SortFilesBySize, undefined);
  });
  const names = () => screen.getAllByRole("listitem").map((row) => row.textContent?.match(/file-\d+/)?.[0]);
  expect(names()).toEqual(["file-1", "file-2", "file-3", "file-4"]);
  expect(sortedSizes).toContain(4);
  const refresh = stream();
  list.mockReturnValue(refresh);
  await act(async () => {
    void browser.refresh();
  });
  sortedSizes.length = 0;
  await refresh.send(rows(2, 2).reverse(), 4n);
  await refresh.send(rows(0, 2).reverse());
  await paint();
  expect(names()).toEqual(["file-4", "file-3", "file-2", "file-1"]);
  expect(sortedSizes).toEqual([]);
  await refresh.finish();
  expect(names()).toEqual(["file-1", "file-2", "file-3", "file-4"]);
  expect(sortedSizes).toContain(4);
});

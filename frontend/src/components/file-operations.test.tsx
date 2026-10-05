import { act, render, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import {
  KeepIdenticalRequest,
  FileOperationKind,
  FileOperationOutcome,
  FileOperationRef,
  FileOperationSpec,
  FileOperationSummary,
  Location,
  LocationEntry,
  LocationEntryRef,
} from "@/entity";
import { canPasteFiles, fileOperationReference, useFileOperations } from "./file-operations";
import { filesEntryData } from "./files-browser";
import { EntryKind, FilesEntry } from "@/entity";
import { locationEntryFile } from "./location-files";
import { operationResponse } from "@/test/files-fixture";

const { execute, notify, dismiss, report } = vi.hoisted(() => ({ execute: vi.fn(), notify: vi.fn(), dismiss: vi.fn(), report: vi.fn() }));
vi.mock("@/api", async (original) => ({
  ...(await original<typeof import("@/api")>()),
  filesCli: { mkdir: execute, move: execute, remove: execute, keepIdentical: execute },
}));
vi.mock("react-toastify", () => ({ toast: { info: notify, dismiss, error: report } }));
const location = LocationEntryRef.create({ locationId: 3n, path: "a.txt", facts: { mode: 420, sizeBytes: 4n, mtimeNs: 100n } });
const references = [
  FileOperationRef.create({ target: { oneofKind: "fileId", fileId: 8n } }),
  FileOperationRef.create({ target: { oneofKind: "location", location } }),
];
const summary = FileOperationSummary.create({ totalItemCount: 1n, totalBytes: 4n, succeededCount: 1n, completed: true });
const stream = (updates: ReturnType<typeof operationResponse>[]) => ({
  responses: (async function* () {
    yield* updates;
  })(),
});
beforeEach(() => {
  execute.mockReset().mockImplementation(() => stream([operationResponse({ summary })]));
  notify.mockReset().mockReturnValue("operation-notice");
  dismiss.mockReset();
  report.mockReset();
});

it("allows Paste only for nonempty movable clipboard entries in the writable destination's source", () => {
  const row = (reference: FileOperationRef, directory = false) =>
    filesEntryData(
      FilesEntry.create({
        reference,
        kind: directory ? EntryKind.DIRECTORY : EntryKind.FILE,
        operations: directory ? [FileOperationKind.MKDIR] : [FileOperationKind.MOVE],
      }),
    );
  const libraryDestination = row(references[0], true);
  const liveDestination = row(references[1], true);
  const libraryFile = row(references[0]);
  const liveFile = row(references[1]);
  const clipboard = (files: (typeof libraryFile)[]) => ({ kind: FileOperationKind.MOVE, files });
  expect(canPasteFiles(undefined, libraryDestination)).toBe(false);
  expect(canPasteFiles(clipboard([]), libraryDestination)).toBe(false);
  expect(canPasteFiles(clipboard([libraryFile]), libraryDestination)).toBe(true);
  expect(canPasteFiles(clipboard([liveFile]), liveDestination)).toBe(true);
  expect(canPasteFiles(clipboard([libraryFile]), liveDestination)).toBe(false);
  expect(canPasteFiles(clipboard([liveFile]), libraryDestination)).toBe(false);
  expect(canPasteFiles(clipboard([libraryFile, liveFile]), libraryDestination)).toBe(false);
  expect(canPasteFiles(clipboard([{ ...libraryFile, allowedOperations: [] }]), libraryDestination)).toBe(false);
  expect(canPasteFiles(clipboard([libraryFile]), { ...libraryDestination, allowedOperations: [] })).toBe(false);
  expect(canPasteFiles(clipboard([libraryFile]), libraryFile)).toBe(false);
  const other = FileOperationRef.clone(references[1]);
  if (other.target.oneofKind === "location") other.target.location.locationId = 99n;
  expect(canPasteFiles(clipboard([liveFile]), row(other, true))).toBe(false);
});

it.each(references)("uses the same request and refresh lifecycle for $target.oneofKind without short-operation feedback", async (reference) => {
  for (const kind of [FileOperationKind.MKDIR, FileOperationKind.MOVE, FileOperationKind.REMOVE]) {
    const refresh = vi.fn().mockResolvedValue(undefined);
    const { result, unmount } = renderHook(() => useFileOperations(refresh));
    const spec = FileOperationSpec.create({ kind, sources: [reference], destination: kind === FileOperationKind.REMOVE ? undefined : reference });
    await act(async () => result.current.start(spec));
    expect(execute).toHaveBeenLastCalledWith(
      kind === FileOperationKind.REMOVE
        ? { sources: spec.sources, dryrun: false }
        : kind === FileOperationKind.MKDIR
          ? { destination: spec.destination, name: spec.name, dryrun: false }
          : { sources: spec.sources, destination: spec.destination, name: spec.name, dryrun: false },
      { abort: expect.any(AbortSignal) },
    );
    expect(refresh).toHaveBeenCalledOnce();
    expect(notify).not.toHaveBeenCalled();
    expect(report).not.toHaveBeenCalled();
    unmount();
  }
});

it("awaits the whole stream, rejects competing operations and never inserts a page progress node", async () => {
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  execute.mockReturnValue({
    responses: (async function* () {
      yield operationResponse({ summary: { totalItemCount: 1n } });
      await pending;
      yield operationResponse({ summary });
    })(),
  });
  const refresh = vi.fn().mockResolvedValue(undefined);
  const { result } = renderHook(() => useFileOperations(refresh));
  let operation!: Promise<void>;
  await act(async () => {
    operation = result.current.start(FileOperationSpec.create({ kind: FileOperationKind.MOVE, sources: [references[0]], destination: references[0] }));
  });
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  expect(result.current).not.toHaveProperty("progress");
  expect(refresh).not.toHaveBeenCalled();
  await expect(result.current.start(FileOperationSpec.create())).rejects.toThrow("in progress");
  await act(async () => {
    release();
    await operation;
  });
  expect(refresh).toHaveBeenCalledOnce();
});

it.each(references)("refreshes partial success then rejects with the same actionable error for $target.oneofKind", async (reference) => {
  execute.mockReturnValue(
    stream([
      operationResponse({ entry: { sourcePath: "b.txt", outcome: FileOperationOutcome.FAILED, error: "Target exists" } }),
      operationResponse({ summary: { ...summary, totalItemCount: 2n, failedCount: 1n } }),
    ]),
  );
  const refresh = vi.fn().mockResolvedValue(undefined);
  const { result } = renderHook(() => useFileOperations(refresh));
  await act(async () =>
    expect(result.current.start(FileOperationSpec.create({ kind: FileOperationKind.REMOVE, sources: [reference] }))).rejects.toThrow(
      "1 failed: b.txt: Target exists",
    ),
  );
  expect(refresh).toHaveBeenCalledOnce();
});

it("refreshes an interrupted stream and does not treat a missing final summary as success", async () => {
  const refresh = vi.fn().mockResolvedValue(undefined);
  const { result } = renderHook(() => useFileOperations(refresh));
  const spec = FileOperationSpec.create({ kind: FileOperationKind.REMOVE, sources: [references[0]] });
  execute.mockReturnValue({
    responses: (async function* () {
      yield operationResponse({ entry: { sourcePath: "a.txt", outcome: FileOperationOutcome.SUCCEEDED } });
      throw new Error("Connection lost");
    })(),
  });
  await expect(result.current.start(spec)).rejects.toThrow("Connection lost");
  execute.mockReturnValue(stream([operationResponse({ summary: { totalItemCount: 1n } })]));
  await expect(result.current.start(spec)).rejects.toThrow("Operation did not complete");
  expect(refresh).toHaveBeenCalledTimes(2);
});

it("reports a successful mutation's refresh failure without inviting a repeated mutation", async () => {
  const { result } = renderHook(() => useFileOperations(vi.fn().mockRejectedValue(new Error("Directory unavailable"))));
  await expect(result.current.start(FileOperationSpec.create({ kind: FileOperationKind.MKDIR, destination: references[0] }))).resolves.toBeUndefined();
  expect(report).toHaveBeenCalledWith("Operation succeeded, but refresh failed: Directory unavailable");
});

it("a submitted Rename shows pending feedback without a Cancel action", async () => {
  let complete!: () => void;
  const pending = new Promise<void>((resolve) => {
    complete = resolve;
  });
  execute.mockReturnValue({
    responses: (async function* () {
      await pending;
      yield operationResponse({ summary });
    })(),
  });
  const { result } = renderHook(() => useFileOperations(vi.fn().mockResolvedValue(undefined)));
  const operation = result.current.start(
    FileOperationSpec.create({
      kind: FileOperationKind.MOVE,
      sources: [references[1]],
      destination: references[1],
      name: "renamed.txt",
    }),
  );
  await waitFor(() => expect(notify).toHaveBeenCalledOnce());
  render(notify.mock.calls[0][0]);
  expect(screen.getByText("Renaming…")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Cancel" })).not.toBeInTheDocument();
  await act(async () => {
    complete();
    await operation;
  });
  expect(dismiss).toHaveBeenCalledWith("operation-notice");
});

it("only a longer batch request exposes a fixed, cancellable notification", async () => {
  let signal: AbortSignal | undefined;
  execute.mockImplementation((_request, options) => {
    signal = options.abort;
    return {
      responses: (async function* () {
        yield operationResponse({ summary: { totalItemCount: 1n } });
        await new Promise((_, reject) => signal!.addEventListener("abort", () => reject(new Error("aborted")), { once: true }));
      })(),
    };
  });
  const refresh = vi.fn().mockResolvedValue(undefined);
  const { result } = renderHook(() => useFileOperations(refresh));
  const operation = result.current
    .start(FileOperationSpec.create({ kind: FileOperationKind.MOVE, sources: [references[1]], destination: references[1] }))
    .catch((error: Error) => error);
  expect(notify).not.toHaveBeenCalled();
  await waitFor(() => expect(notify).toHaveBeenCalledOnce());
  render(notify.mock.calls[0][0]);
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect((await operation)?.message).toBe("Operation canceled");
  expect(refresh).toHaveBeenCalledOnce();
  expect(dismiss).toHaveBeenCalledWith("operation-notice");
});

it("aborts an active request on unmount without refreshing the abandoned page", async () => {
  let signal: AbortSignal | undefined;
  execute.mockImplementation((_request, options) => {
    signal = options.abort;
    return {
      responses: (async function* () {
        yield operationResponse({ summary: { totalItemCount: 1n } });
        await new Promise((_, reject) => signal!.addEventListener("abort", () => reject(new Error("aborted")), { once: true }));
      })(),
    };
  });
  const refresh = vi.fn();
  const { result, unmount } = renderHook(() => useFileOperations(refresh));
  let operation!: Promise<void | Error>;
  await act(async () => {
    operation = result.current.start(FileOperationSpec.create({ kind: FileOperationKind.REMOVE, sources: [references[0]] })).catch((error: Error) => error);
  });
  unmount();
  expect(signal?.aborted).toBe(true);
  expect((await operation)?.message).toBe("Operation canceled");
  expect(refresh).not.toHaveBeenCalled();
});

it("rejects cross-source transfers and unsupported Library copy rather than moving files", async () => {
  const { result } = renderHook(() => useFileOperations(vi.fn()));
  const other = FileOperationRef.create({ target: { oneofKind: "location", location: { ...location, locationId: 4n } } });
  await expect(result.current.start(FileOperationSpec.create({ kind: FileOperationKind.MOVE, sources: [references[1]], destination: other }))).rejects.toThrow(
    "same source",
  );
  await expect(
    result.current.start(FileOperationSpec.create({ kind: FileOperationKind.MOVE, sources: [references[1]], destination: references[0] })),
  ).rejects.toThrow("same source");
  await expect(
    result.current.start(FileOperationSpec.create({ kind: 999 as FileOperationKind, sources: [references[0]], destination: references[0] })),
  ).rejects.toThrow("not supported");
  expect(execute).not.toHaveBeenCalled();
});

it.each(["library", "location"])("shares cut/paste for %s and clears its clipboard only after success", async (scope) => {
  const { result } = renderHook(() => useFileOperations(vi.fn()));
  const source =
    scope === "library"
      ? { id: "8", name: "a.txt" }
      : locationEntryFile(LocationEntry.create({ path: location.path, reference: location }), Location.create({ id: 3n }));
  const target = fileOperationReference(source);
  act(() => result.current.setClipboard({ kind: FileOperationKind.MOVE, files: [source] }));
  execute.mockReturnValueOnce(stream([operationResponse({ summary: { ...summary, failedCount: 1n } })]));
  await expect(result.current.paste(target)).rejects.toThrow("1 failed");
  expect(result.current.clipboard?.files).toEqual([source]);
  await act(async () => result.current.paste(target));
  expect(result.current.clipboard).toBeUndefined();
});

it("shares Keep streaming outcomes and refresh without converting its authoritative scope to loaded Remove references", async () => {
  const refresh = vi.fn().mockResolvedValue(undefined);
  const { result } = renderHook(() => useFileOperations(refresh));
  const request = KeepIdenticalRequest.create({ groupId: "g", fingerprint: "full-group", keep: references[1] });
  await act(async () => result.current.startKeep(request));
  expect(execute).toHaveBeenCalledWith(request, { abort: expect.any(AbortSignal) });
  expect(refresh).toHaveBeenCalledTimes(1);
});

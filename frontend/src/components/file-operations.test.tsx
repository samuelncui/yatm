import { act, render, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import {
  FileOperationKind,
  FileOperationOutcome,
  FileOperationRef,
  FileOperationSpec,
  FileOperationSummary,
  FileOperationUpdate,
  Location,
  LocationEntry,
  LocationEntryRef,
} from "@/entity";
import { fileOperationReference, useFileOperations } from "./file-operations";
import { locationEntryFile } from "./location-files";

const { execute, notify, dismiss, report } = vi.hoisted(() => ({ execute: vi.fn(), notify: vi.fn(), dismiss: vi.fn(), report: vi.fn() }));
vi.mock("@/api", async (original) => ({ ...(await original<typeof import("@/api")>()), fileOperationCli: { execute } }));
vi.mock("react-toastify", () => ({ toast: { info: notify, dismiss, error: report } }));
const location = LocationEntryRef.create({ locationId: 3n, path: "a.txt", bindingToken: "binding", facts: { mode: 420, size: 4n, mtimeNs: 100n } });
const references = [
  FileOperationRef.create({ target: { oneofKind: "fileId", fileId: 8n } }),
  FileOperationRef.create({ target: { oneofKind: "location", location } }),
];
const summary = FileOperationSummary.create({ totalItems: 1n, totalBytes: 4n, succeeded: 1n, completed: true });
const stream = (updates: FileOperationUpdate[]) => ({
  responses: (async function* () {
    yield* updates;
  })(),
});
beforeEach(() => {
  execute.mockReset().mockImplementation(() => stream([FileOperationUpdate.create({ summary })]));
  notify.mockReset().mockReturnValue("operation-notice");
  dismiss.mockReset();
  report.mockReset();
});

it.each(references)("uses the same request and refresh lifecycle for $target.oneofKind without short-operation feedback", async (reference) => {
  for (const kind of [FileOperationKind.MAKE_DIRECTORY, FileOperationKind.MOVE, FileOperationKind.DELETE]) {
    const refresh = vi.fn().mockResolvedValue(undefined);
    const { result, unmount } = renderHook(() => useFileOperations(refresh));
    const spec = FileOperationSpec.create({ kind, sources: [reference], destination: kind === FileOperationKind.DELETE ? undefined : reference });
    await act(async () => result.current.start(spec));
    expect(execute).toHaveBeenLastCalledWith(
      { spec, confirmDelete: kind === FileOperationKind.DELETE && reference.target.oneofKind === "location" },
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
      yield FileOperationUpdate.create({ summary: { totalItems: 1n } });
      await pending;
      yield FileOperationUpdate.create({ summary });
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
      FileOperationUpdate.create({ entry: { sourcePath: "b.txt", outcome: FileOperationOutcome.FAILED, error: "Target exists" } }),
      FileOperationUpdate.create({ summary: { ...summary, totalItems: 2n, failed: 1n } }),
    ]),
  );
  const refresh = vi.fn().mockResolvedValue(undefined);
  const { result } = renderHook(() => useFileOperations(refresh));
  await act(async () =>
    expect(result.current.start(FileOperationSpec.create({ kind: FileOperationKind.DELETE, sources: [reference] }))).rejects.toThrow(
      "1 failed: b.txt: Target exists",
    ),
  );
  expect(refresh).toHaveBeenCalledOnce();
});

it("refreshes an interrupted stream and does not treat a missing final summary as success", async () => {
  const refresh = vi.fn().mockResolvedValue(undefined);
  const { result } = renderHook(() => useFileOperations(refresh));
  const spec = FileOperationSpec.create({ kind: FileOperationKind.DELETE, sources: [references[0]] });
  execute.mockReturnValue({
    responses: (async function* () {
      yield FileOperationUpdate.create({ entry: { sourcePath: "a.txt", outcome: FileOperationOutcome.SUCCEEDED } });
      throw new Error("Connection lost");
    })(),
  });
  await expect(result.current.start(spec)).rejects.toThrow("Connection lost");
  execute.mockReturnValue(stream([FileOperationUpdate.create({ summary: { totalItems: 1n } })]));
  await expect(result.current.start(spec)).rejects.toThrow("Operation did not complete");
  expect(refresh).toHaveBeenCalledTimes(2);
});

it("reports a successful mutation's refresh failure without inviting a repeated mutation", async () => {
  const { result } = renderHook(() => useFileOperations(vi.fn().mockRejectedValue(new Error("Directory unavailable"))));
  await expect(result.current.start(FileOperationSpec.create({ kind: FileOperationKind.MAKE_DIRECTORY, destination: references[0] }))).resolves.toBeUndefined();
  expect(report).toHaveBeenCalledWith("Operation succeeded, but refresh failed: Directory unavailable");
});

it("only a longer request exposes a fixed, cancellable notification", async () => {
  let signal: AbortSignal | undefined;
  execute.mockImplementation((_request, options) => {
    signal = options.abort;
    return {
      responses: (async function* () {
        yield FileOperationUpdate.create({ summary: { totalItems: 1n } });
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
        yield FileOperationUpdate.create({ summary: { totalItems: 1n } });
        await new Promise((_, reject) => signal!.addEventListener("abort", () => reject(new Error("aborted")), { once: true }));
      })(),
    };
  });
  const refresh = vi.fn();
  const { result, unmount } = renderHook(() => useFileOperations(refresh));
  let operation!: Promise<void | Error>;
  await act(async () => {
    operation = result.current.start(FileOperationSpec.create({ kind: FileOperationKind.DELETE, sources: [references[0]] })).catch((error: Error) => error);
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
    result.current.start(FileOperationSpec.create({ kind: FileOperationKind.COPY, sources: [references[0]], destination: references[0] })),
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
  execute.mockReturnValueOnce(stream([FileOperationUpdate.create({ summary: { ...summary, failed: 1n } })]));
  await expect(result.current.paste(target)).rejects.toThrow("1 failed");
  expect(result.current.clipboard?.files).toEqual([source]);
  await act(async () => result.current.paste(target));
  expect(result.current.clipboard).toBeUndefined();
});

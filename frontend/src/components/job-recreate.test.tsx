import type { PropsWithChildren, ReactNode } from "react";
import { act, screen, waitFor } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import { Link, MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, expect, it, vi } from "vitest";
import {
  CreateArchiveJobRequest,
  CreateRestoreJobRequest,
  CreateScanJobRequest,
  EntryKind,
  FileOperationKind,
  FileScope,
  FileSelection,
  FilesDetail,
  FileVersion,
  Location,
  Media,
  MediaAccess,
  MediaKind,
  PreviewPolicy,
  ScanResultPolicy,
  ScanSignaturePolicy,
  SelectionInspectionResult,
  type FileOperationRef,
} from "@/entity";
import { createAppStore } from "@/state/store";
import { addSelectionEntries, restorePolicyCodec, restorePolicyKey, selectionActions, type SelectionEntry } from "@/state/selections";
import { minUnixNs, maxUnixNs } from "@/tools/time";
import { restoreCutoff } from "./restore-version-policy";
import { ArchiveBrowser } from "@/pages/archive";
import { RestoreBrowser } from "@/pages/restore";
import { ScanBrowser } from "@/pages/scan";
import { readCreationEntries } from "./job-creation";

const mocks = vi.hoisted(() => ({
  archiveRead: vi.fn(),
  restoreRead: vi.fn(),
  scanRead: vi.fn(),
  archiveCreate: vi.fn(),
  restoreCreate: vi.fn(),
  scanCreate: vi.fn(),
  archiveEstimate: vi.fn(),
  restoreEstimate: vi.fn(),
  get: vi.fn(),
  version: vi.fn(),
  location: vi.fn(),
  media: vi.fn(),
}));
vi.mock("@/api", () => ({
  archiveJobCli: { getCreation: mocks.archiveRead, create: mocks.archiveCreate, estimate: mocks.archiveEstimate },
  restoreJobCli: { getCreation: mocks.restoreRead, create: mocks.restoreCreate, estimate: mocks.restoreEstimate },
  scanJobCli: { getCreation: mocks.scanRead, create: mocks.scanCreate },
  filesCli: { get: mocks.get, getVersion: mocks.version },
  locationCli: { get: mocks.location },
  mediaCli: { list: mocks.media },
}));
vi.mock("@/pages/file", () => ({
  useFileBrowser: () => ({ refresh: async () => {}, scope: FileScope.ALL, files: [], selector: null, browserProps: { onFileAction: vi.fn() } }),
}));
vi.mock("@samuelncui/chonky", async (original) => {
  const actual = await original<typeof import("@samuelncui/chonky")>();
  const { forwardRef } = await import("react");
  return {
    ...actual,
    FileBrowser: forwardRef(({ children }: PropsWithChildren, _ref) => <div>{children}</div>),
    FileNavbar: () => null,
    FileToolbar: () => null,
    FileList: () => null,
    FileContextMenu: () => null,
  };
});
vi.mock("./selection-waitlist", () => ({
  waitlistRootID: (kind: string) => `waitlist:${kind}`,
  SelectionWaitlist: ({ entries, onRemove, footer }: { entries: SelectionEntry[]; onRemove: (keys: Set<string>) => void; footer: ReactNode }) => (
    <div>
      {entries.map((entry) => (
        <div key={entry.key}>
          <span>{entry.name}</span>
          <span>{entry.unavailableReason}</span>
          <button onClick={() => onRemove(new Set([entry.key]))}>Remove {entry.name}</button>
        </div>
      ))}
      {footer}
    </div>
  ),
}));
vi.mock("./preview-policy-select", () => ({ usePreviewGeneration: () => ({ available: true }), PreviewPolicySelect: () => null }));
vi.mock("./restore-time-picker", () => ({
  default: ({ value, onChange }: { value: string; onChange: (value: string) => void }) => (
    <input aria-label="Restore time" value={value} onChange={(event) => onChange(event.target.value)} />
  ),
}));
vi.mock("./catalog-search-select", () => ({ LocationSearchSelect: () => null, MediaSearchSelect: () => null }));
vi.mock("./scan-selection-dialog", () => ({ ScanSelectionDialog: () => null }));

const call = (value: unknown) => ({ response: Promise.resolve(value) });
const selection = (id: bigint, scope = FileScope.ALL) => FileSelection.create({ target: { oneofKind: "library", library: { fileId: id } }, scope });
const live = FileSelection.create({ target: { oneofKind: "location", location: { locationId: 8n, path: "only/this" } }, scope: FileScope.ALL });
const detail = (reference: FileOperationRef) =>
  FilesDetail.create({
    entry: {
      reference,
      name: reference.target.oneofKind === "fileId" ? `File ${reference.target.fileId}` : "Selected folder",
      kind: EntryKind.DIRECTORY,
      operations: [FileOperationKind.MKDIR, FileOperationKind.ARCHIVE],
    },
  });
const show = (path: string, store = createAppStore()) => ({
  store,
  ...render(
    <MemoryRouter initialEntries={[path]}>
      <Link to="/archive?recreate=2">Another Job</Link>
      <Link to="/jobs">Leave</Link>
      <Routes>
        <Route path="/archive" element={<ArchiveBrowser />} />
        <Route path="/restore" element={<RestoreBrowser />} />
        <Route path="/scan" element={<ScanBrowser />} />
        <Route path="/jobs/*" element={<p>Jobs</p>} />
      </Routes>
    </MemoryRouter>,
    { store },
  ),
});

beforeEach(() => {
  vi.resetAllMocks();
  sessionStorage.clear();
  localStorage.clear();
  mocks.get.mockImplementation(({ reference }: { reference: FileOperationRef }) => call({ detail: detail(reference) }));
  mocks.location.mockImplementation(({ id }: { id: bigint }) => call({ location: Location.create({ id, name: `Location ${id}`, rootPath: `/target/${id}` }) }));
  mocks.version.mockImplementation(({ id }: { id: bigint }) => call({ version: FileVersion.create({ id, fileId: 12n, sizeBytes: 14n }) }));
  const estimate = ({ selections }: { selections: FileSelection[] }) =>
    call({ result: SelectionInspectionResult.create({ selections, fileCount: 2n, totalBytes: 10n }) });
  mocks.archiveEstimate.mockImplementation(estimate);
  mocks.restoreEstimate.mockImplementation(estimate);
  for (const create of [mocks.archiveCreate, mocks.restoreCreate, mocks.scanCreate]) create.mockReturnValue(call({ job: { id: 90n } }));
});

it("opens Archive without submitting and preserves roots, scope, Preview, force rehash and priority through a fresh estimate", async () => {
  const request = CreateArchiveJobRequest.create({
    priority: 23n,
    spec: { selections: [selection(7n, FileScope.SAVED), live] },
    previewPolicy: PreviewPolicy.REGENERATE_ALL,
    forceRehash: true,
  });
  mocks.archiveRead.mockReturnValue(call({ request, unavailableReason: "" }));
  show("/archive?recreate=1");
  await screen.findByText("File 7");
  expect(mocks.archiveCreate).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Archive…" }));
  expect(screen.getByRole("checkbox", { name: "Force rehash" })).toBeChecked();
  await waitFor(() => expect(screen.getByRole("button", { name: "Prepare archive" })).toBeEnabled());
  expect(mocks.archiveEstimate.mock.calls[0][0].selections).toEqual(request.spec!.selections);
  await userEvent.click(screen.getByRole("button", { name: "Prepare archive" }));
  expect(mocks.archiveCreate).toHaveBeenCalledExactlyOnceWith(request);
});

it.each([1700000000789123456n, -1n, 0n, minUnixNs, maxUnixNs])(
  "preserves Restore's exact cutoff %s, versions, destination and options until manual submission",
  async (cutoff) => {
    localStorage.setItem("restore:last-target", JSON.stringify({ locationID: "99", rootPath: "/other", path: "unrelated" }));
    const request = CreateRestoreJobRequest.create({
      priority: 27n,
      spec: {
        selections: [selection(7n)],
        fileVersionIds: [17n, 18n],
        destination: { locationId: 5n, path: "chosen" },
        allowDamagedCopies: true,
        versionPolicy: { beforeAtNs: cutoff },
        skipUnmatchedVersions: true,
      },
    });
    mocks.restoreRead.mockReturnValue(call({ request, unavailableReason: "" }));
    const { store } = show("/restore?recreate=1");
    await screen.findByText("File 7");
    expect(restoreCutoff(store.getState().selections.versionPolicy)).toBe(cutoff);
    expect(mocks.restoreCreate).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Restore…" }));
    await screen.findByText("Location 5 / chosen");
    expect(screen.getByRole("checkbox", { name: "Allow damaged copies" })).toBeChecked();
    await waitFor(() => expect(screen.getByRole("button", { name: "Prepare restore" })).toBeEnabled());
    expect(mocks.location.mock.calls.map(([arg]) => arg.id)).toEqual([5n]);
    expect(mocks.restoreEstimate.mock.calls.at(-1)![0].versionPolicy.beforeAtNs).toBe(cutoff);
    await userEvent.click(screen.getByRole("button", { name: "Prepare restore" }));
    expect(mocks.restoreCreate).toHaveBeenCalledExactlyOnceWith(request);
  },
);

it("keeps unavailable roots and explicit version IDs visible and blocks creation until removed", async () => {
  mocks.restoreRead.mockReturnValue(
    call({
      request: CreateRestoreJobRequest.create({ spec: { selections: [selection(7n)], fileVersionIds: [404n], destination: { locationId: 5n } } }),
      unavailableReason: "",
    }),
  );
  mocks.version.mockReturnValue(call({}));
  const { store } = show("/restore?recreate=1");
  await screen.findByText("Saved version 404");
  const missing = store.getState().selections.restore.entries.find((entry) => entry.versionID === "404")!;
  expect(missing.version).toBeUndefined();
  expect(missing.fileID).toBeUndefined();
  expect(missing.isDir).toBeUndefined();
  await userEvent.click(screen.getByRole("button", { name: "Restore…" }));
  expect(screen.getByRole("button", { name: "Prepare restore" })).toBeDisabled();
  expect(mocks.restoreEstimate).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  await userEvent.click(await screen.findByRole("button", { name: "Remove Saved version 404" }));
  await userEvent.click(screen.getByRole("button", { name: "Restore…" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Prepare restore" })).toBeEnabled());
  expect(mocks.restoreCreate).not.toHaveBeenCalled();
});

it("requires confirmation before replacing unsent selections and cancellation retains the current draft", async () => {
  const store = createAppStore();
  const current = selection(55n);
  addSelectionEntries(store, "archive", [{ selection: current, name: "Unsent", path: "Unsent", key: FileSelection.toJsonString(current) }]);
  mocks.archiveRead.mockReturnValue(call({ request: CreateArchiveJobRequest.create({ spec: { selections: [selection(7n)] } }), unavailableReason: "" }));
  show("/archive?recreate=1", store);
  await screen.findByRole("dialog", { name: "Replace unsent selections?" });
  expect(store.getState().selections.archive.entries[0].name).toBe("Unsent");
  await userEvent.click(screen.getByRole("link", { name: "Cancel" }));
  expect(await screen.findByText("Unsent")).toBeInTheDocument();
  expect(mocks.archiveCreate).not.toHaveBeenCalled();
});

it("retains concurrent selection edits while the read is pending and requires explicit replacement", async () => {
  let resolve!: (value: unknown) => void;
  mocks.archiveRead.mockReturnValue({
    response: new Promise((done) => {
      resolve = done;
    }),
  });
  const { store } = show("/archive?recreate=1");
  act(() => addSelectionEntries(store, "archive", [{ selection: selection(55n), name: "Concurrent", path: "Concurrent", key: "concurrent" }]));
  await act(async () => resolve({ request: CreateArchiveJobRequest.create({ spec: { selections: [selection(7n)] } }), unavailableReason: "" }));
  await screen.findByRole("dialog", { name: "Replace unsent selections?" });
  expect(store.getState().selections.archive.entries[0].name).toBe("Concurrent");
  await userEvent.click(screen.getByRole("button", { name: "Replace selections" }));
  await screen.findByText("File 7");
  expect(store.getState().selections.archive.entries).toHaveLength(1);
  expect(mocks.archiveCreate).not.toHaveBeenCalled();
});

it("aborts obsolete reads across repeated opens without replacing the newer draft", async () => {
  let resolve!: (value: unknown) => void;
  mocks.archiveRead.mockReturnValueOnce({
    response: new Promise((done) => {
      resolve = done;
    }),
  });
  mocks.archiveRead.mockReturnValueOnce(call({ request: CreateArchiveJobRequest.create({ spec: { selections: [selection(8n)] } }), unavailableReason: "" }));
  const { store } = show("/archive?recreate=1");
  const signal = mocks.archiveRead.mock.calls[0][1].abort as AbortSignal;
  await userEvent.click(screen.getByRole("link", { name: "Another Job" }));
  await screen.findByText("File 8");
  expect(signal.aborted).toBe(true);
  await act(async () => resolve({ request: CreateArchiveJobRequest.create({ spec: { selections: [selection(7n)] } }), unavailableReason: "" }));
  expect(store.getState().selections.archive.entries[0].name).toBe("File 8");
  expect(mocks.get).toHaveBeenCalledTimes(1);
});

it("shows incomplete imported inputs and requires a new selection instead of widening the scope", async () => {
  mocks.archiveRead.mockReturnValue(
    call({
      request: CreateArchiveJobRequest.create({ priority: 4n, previewPolicy: PreviewPolicy.NONE }),
      unavailableReason: "Imported Job has no recorded selections.",
    }),
  );
  const { store } = show("/archive?recreate=1");
  await screen.findByText("Imported Job has no recorded selections.");
  expect(store.getState().selections.archive.entries).toHaveLength(0);
  expect(screen.getByRole("button", { name: "Archive…" })).toBeDisabled();
  expect(mocks.archiveCreate).not.toHaveBeenCalled();
});

it("recreates Scan selected roots and all options through the normal explicit Start action", async () => {
  const request = CreateScanJobRequest.create({
    priority: 51n,
    spec: {
      selections: [live, selection(7n, FileScope.SAVED)],
      signaturePolicy: ScanSignaturePolicy.FILL_MISSING,
      resultPolicy: ScanResultPolicy.PUBLISH_ORIGINALS,
      compareLibrary: true,
      previewPolicy: PreviewPolicy.REGENERATE_ALL,
    },
  });
  mocks.scanRead.mockReturnValue(call({ request, unavailableReason: "" }));
  show("/scan?recreate=1");
  await screen.findByText("2 selected roots");
  expect(mocks.scanCreate).not.toHaveBeenCalled();
  expect(screen.getByRole("checkbox", { name: "Find matching Library content" })).toBeChecked();
  await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
  expect(mocks.scanCreate).toHaveBeenCalledExactlyOnceWith(request);
});

it("retains a failed Scan root and blocks Start without broadening it to the entire Location", async () => {
  mocks.scanRead.mockReturnValue(
    call({
      request: CreateScanJobRequest.create({
        spec: {
          selections: [live],
          signaturePolicy: ScanSignaturePolicy.KNOWN_ONLY,
          resultPolicy: ScanResultPolicy.REPORT_ONLY,
          previewPolicy: PreviewPolicy.NONE,
        },
      }),
      unavailableReason: "",
    }),
  );
  mocks.get.mockImplementation(() => ({ response: Promise.reject(new Error("Location unavailable")) }));
  show("/scan?recreate=1");
  await screen.findByText(/Location unavailable.*Remove it or select it again/);
  expect(screen.getByRole("button", { name: "Start scan" })).toBeDisabled();
  expect(mocks.scanCreate).not.toHaveBeenCalled();
  expect(mocks.location).not.toHaveBeenCalled();
});

it("retains Scan Media and verification options", async () => {
  const request = CreateScanJobRequest.create({
    priority: 9n,
    spec: {
      mediaId: 15n,
      signaturePolicy: ScanSignaturePolicy.FORCE_READ,
      resultPolicy: ScanResultPolicy.VERIFY_COPIES,
      compareLibrary: false,
      previewPolicy: PreviewPolicy.NONE,
    },
  });
  mocks.scanRead.mockReturnValue(call({ request, unavailableReason: "" }));
  mocks.media.mockReturnValue(call({ media: [Media.create({ id: 15n, kind: MediaKind.VOLUME, capabilities: { read: MediaAccess.RANDOM } })] }));
  show("/scan?recreate=1");
  await waitFor(() => expect(screen.getByRole("button", { name: "Start scan" })).toBeEnabled());
  expect(mocks.scanCreate).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
  expect(mocks.scanCreate).toHaveBeenCalledExactlyOnceWith(request);
});

it("resolves at most twenty roots concurrently and stops before another page after cancellation", async () => {
  const pending: (() => void)[] = [];
  mocks.get.mockImplementation(({ reference }: { reference: FileOperationRef }) => ({
    response: new Promise((resolve) => {
      pending.push(() => resolve({ detail: detail(reference) }));
    }),
  }));
  const controller = new AbortController();
  const promise = readCreationEntries(
    Array.from({ length: 45 }, (_, index) => selection(BigInt(index + 1))),
    [],
    controller.signal,
  );
  const caught = promise.catch((failure: unknown) => failure);
  expect(mocks.get).toHaveBeenCalledTimes(20);
  controller.abort();
  pending.forEach((resolve) => resolve());
  expect(await caught).toMatchObject({ name: "AbortError" });
  expect(mocks.get).toHaveBeenCalledTimes(20);
});

it("preserves a cutoff beyond Date precision until the operator edits the displayed date", () => {
  const exact = 1700000000789123456n;
  const policy = { mode: "before" as const, date: "", cutoff: { valueNs: String(exact), date: "" } };
  expect(restoreCutoff(policy)).toBe(exact);
  expect(restoreCutoff({ ...policy, date: "2026-10-04T00:00:00.123Z" })).toBe(1791072000123000000n);
  const store = createAppStore();
  store.dispatch(selectionActions.policyChanged(policy));
  expect(restoreCutoff(createAppStore().getState().selections.versionPolicy)).toBe(exact);
});

it("rejects pre-ns and out-of-range persisted cutoffs instead of reinterpreting or rounding them", () => {
  const date = "2023-11-14T22:13:20.789Z";
  for (const cutoff of [
    { value: "1700000000789", date },
    { valueNs: String(minUnixNs - 1n), date },
    { valueNs: String(maxUnixNs + 1n), date },
  ]) {
    const raw = JSON.stringify({ mode: "before", date, cutoff });
    expect(() => restorePolicyCodec.decode(raw)).toThrow("Invalid Restore cutoff");
    sessionStorage.setItem(restorePolicyKey, raw);
    expect(restoreCutoff(createAppStore().getState().selections.versionPolicy)).toBeUndefined();
  }
});

it.each([minUnixNs, -1n, 0n, maxUnixNs])("stores an exact signed cutoff %s as a JSON decimal string", (cutoff) => {
  const policy = { mode: "before" as const, date: "", cutoff: { valueNs: String(cutoff), date: "" } };
  const raw = restorePolicyCodec.encode(policy);
  expect(JSON.parse(raw).cutoff.valueNs).toBe(String(cutoff));
  expect(restoreCutoff(restorePolicyCodec.decode(raw))).toBe(cutoff);
});

it("uses an edited cutoff and drops explicit version IDs when the operator applies the current policy", async () => {
  mocks.restoreRead.mockReturnValue(
    call({
      request: CreateRestoreJobRequest.create({
        spec: {
          selections: [selection(7n)],
          fileVersionIds: [17n],
          destination: { locationId: 5n },
          versionPolicy: { beforeAtNs: 1700000000789123456n },
          skipUnmatchedVersions: true,
        },
      }),
      unavailableReason: "",
    }),
  );
  const { store } = show("/restore?recreate=1");
  await screen.findByText("File 7");
  await userEvent.click(screen.getByRole("button", { name: "Restore…" }));
  const time = await screen.findByRole("textbox", { name: "Restore time" });
  await userEvent.clear(time);
  await userEvent.type(time, "2026-10-04T00:00:00.123Z");
  await userEvent.click(screen.getByRole("button", { name: "Apply current policy to all" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Prepare restore" })).toBeEnabled());
  expect(store.getState().selections.versionPolicy.cutoff).toBeUndefined();
  await userEvent.click(screen.getByRole("button", { name: "Prepare restore" }));
  expect(mocks.restoreCreate.mock.calls[0][0].spec).toMatchObject({
    fileVersionIds: [],
    versionPolicy: { beforeAtNs: 1791072000123000000n },
    skipUnmatchedVersions: false,
  });
});

it("preserves missing destination identity without accepting an unrelated remembered target", async () => {
  localStorage.setItem("restore:last-target", JSON.stringify({ locationID: "99", rootPath: "/other", path: "unrelated" }));
  mocks.restoreRead.mockReturnValue(
    call({
      request: CreateRestoreJobRequest.create({ spec: { selections: [selection(7n)], destination: { locationId: 404n, path: "original" } } }),
      unavailableReason: "",
    }),
  );
  mocks.location.mockReturnValue(call({}));
  show("/restore?recreate=1");
  await screen.findByText("File 7");
  await userEvent.click(screen.getByRole("button", { name: "Restore…" }));
  await screen.findByText(/Location 404\/original:.*Choose a destination/);
  expect(screen.getByRole("button", { name: "Choose restore target" })).toHaveTextContent("Choose folder…");
  expect(screen.getByRole("button", { name: "Prepare restore" })).toBeDisabled();
  expect(mocks.location.mock.calls.map(([input]) => input.id)).toEqual([404n]);
  expect(mocks.restoreCreate).not.toHaveBeenCalled();
});

it("retains a metadata error as an incomplete root without inventing File facts", async () => {
  mocks.get.mockReturnValue(call({ detail: FilesDetail.create({ entry: { error: "The selected name cannot be read." } }) }));
  const entries = await readCreationEntries([live], [], new AbortController().signal);
  expect(entries).toHaveLength(1);
  expect(entries[0]).toMatchObject({ selection: live, unavailableReason: "The selected name cannot be read. Remove it or select it again." });
  expect(entries[0].fileID).toBeUndefined();
  expect(entries[0].isDir).toBeUndefined();
  expect(entries[0].size).toBeUndefined();
});

it("lets a deliberate reselection replace an unavailable root while retaining its scope", async () => {
  mocks.archiveRead.mockReturnValue(
    call({ request: CreateArchiveJobRequest.create({ spec: { selections: [selection(7n)] }, previewPolicy: PreviewPolicy.NONE }), unavailableReason: "" }),
  );
  mocks.get.mockReturnValue(call({}));
  const { store } = show("/archive?recreate=1");
  await screen.findByText("Library item 7");
  const original = store.getState().selections.archive.entries[0];
  expect(original.unavailableReason).toBeTruthy();
  act(() => addSelectionEntries(store, "archive", [{ key: original.key, name: "Available again", path: "Available again", selection: selection(7n) }]));
  await screen.findByText("Available again");
  expect(store.getState().selections.archive.entries).toHaveLength(1);
  expect(store.getState().selections.archive.entries[0].unavailableReason).toBeUndefined();
  await userEvent.click(screen.getByRole("button", { name: "Archive…" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Prepare archive" })).toBeEnabled());
  expect(mocks.archiveEstimate.mock.calls[0][0].selections).toEqual([selection(7n)]);
  expect(mocks.archiveCreate).not.toHaveBeenCalled();
});

import { streamListing } from "@/test/files-fixture";
import { act, screen, waitFor, within } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useParams, useSearchParams } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  File,
  FileVersion,
  FileScope,
  FileSelection,
  Position,
  Location,
  SelectionInspectionResult,
  RestoreVersionMatch,
  EntryKind,
  FileOperationKind,
  FilesEntry,
  ListFilesResponse,
  type EstimateRestoreJobRequest,
} from "@/entity";
const { parents, getVersion, listVersions, listCopies, create, list, get, listFiles, inspect, browserFiles } = vi.hoisted(() => ({
  parents: vi.fn(),
  getVersion: vi.fn(),
  listVersions: vi.fn(),
  listCopies: vi.fn(),
  create: vi.fn(),
  list: vi.fn(),
  get: vi.fn(),
  listFiles: vi.fn(),
  inspect: vi.fn(),
  browserFiles: { current: [] as import("@samuelncui/chonky").FileData[] },
}));
vi.mock("@/api", async (original) => ({
  ...(await original<typeof import("@/api")>()),
  filesCli: {
    list: listFiles,
    getVersion,
    listVersions,
    listCopies,
    get: ({ reference }: any) => ({
      response: parents({ id: reference.target.fileId }).response.then((reply: any) => ({
        detail: {
          entry: { name: reply.parents.at(-1)?.name },
          organization: {
            path: reply.parents
              .filter((file: any) => file.id !== 0n)
              .map((file: any) => file.name)
              .join("/"),
          },
        },
      })),
    }),
  },

  restoreJobCli: {
    create,
    estimate: (...args: unknown[]) => ({ response: inspect(...args).response.then((result: unknown) => ({ result })) }),
  },
  locationCli: { list, get },
}));
vi.mock("@/pages/file", () => ({
  useFileBrowser: () => ({
    refresh: async () => {},
    scope: FileScope.SAVED,
    files: browserFiles.current,
    selector: <button>Library</button>,
    browserProps: { files: [], onFileAction: vi.fn() },
  }),
}));
vi.mock("@samuelncui/chonky", async (original) => {
  const actual = await original<typeof import("@samuelncui/chonky")>();
  const { forwardRef } = await import("react");
  return {
    ...actual,
    FileBrowser: forwardRef(({ children, instanceId, onFileAction, files }: any, _ref) => (
      <div>
        {children}
        {instanceId === "select-restore" &&
          files.map((file: any) => (
            <button key={file.id} onClick={() => onFileAction({ id: "add_job_selection", state: { selectedFilesForAction: [file] } })}>
              Add {file.name}
            </button>
          ))}
      </div>
    )),
    FileNavbar: () => null,
    FileToolbar: () => null,
    FileList: () => null,
    FileContextMenu: () => null,
  };
});
import { RestoreBrowser } from "./restore";
import { restoreVersionLabel } from "@/components/restore-version-policy";
vi.mock("@/components/selection-waitlist", async (original) => ({
  ...(await original<typeof import("@/components/selection-waitlist")>()),
  SelectionWaitlist: ({
    entries,
    footer,
    onChooseVersion,
    resolutions,
    cutoff,
  }: {
    entries: import("@/components/selection-waitlist").SelectionEntry[];
    footer?: React.ReactNode;
    onChooseVersion: (entry: import("@/components/selection-waitlist").SelectionEntry) => void;
    resolutions?: import("@/entity").RestoreVersionResolution[];
    cutoff?: bigint;
  }) => (
    <div>
      <span>Selected · {entries.length}</span>
      {entries.map((entry) => (
        <span key={entry.key}>
          {entry.path}
          <span>
            {restoreVersionLabel(
              entry,
              resolutions?.find((resolution) => String(resolution.fileId) === entry.fileID),
              cutoff,
            )}
          </span>
        </span>
      ))}
      {entries.length > 0 && <button onClick={() => onChooseVersion(entries[0])}>Change version</button>}
      {entries.some((entry) => entry.isDir) && (
        <button onClick={() => onChooseVersion({ key: "nested-file", name: "organized.jpg", path: "Trips/organized.jpg", fileID: "7" })}>
          Choose nested file version
        </button>
      )}
      {footer}
    </div>
  ),
}));
const call = (value: unknown) => ({ response: Promise.resolve(value) });
const ChooseAnother = () => {
  const [, setParams] = useSearchParams();
  return (
    <>
      <button onClick={() => setParams({ version_id: "20" })}>Add another version</button>
      <RestoreBrowser />
    </>
  );
};
const Destination = () => <div>Job {useParams().id}</div>;
const show = (route = "/restore?version_id=19") =>
  render(
    <MemoryRouter initialEntries={[route]}>
      <Routes>
        <Route path="/restore" element={<ChooseAnother />} />
        <Route path="/jobs/:id" element={<Destination />} />
      </Routes>
    </MemoryRouter>,
  );
const target = async (path = "") => {
  if (!screen.queryByRole("dialog", { name: "Restore" })) {
    await waitFor(() => expect(screen.getByRole("button", { name: "Restore…" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Restore…" }));
  }
  await userEvent.click(screen.getByRole("button", { name: "Choose restore target" }));
  await waitFor(() => expect(screen.getByRole("combobox", { name: "Location" })).toBeEnabled());
  await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
  await userEvent.click(await screen.findByRole("option", { name: "Recovered files" }));
  if (path) await userEvent.dblClick(await screen.findByRole("button", { name: path }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Choose" })).toBeEnabled());
  await userEvent.click(screen.getByRole("button", { name: "Choose" }));
};
const before = async (date: string) => {
  if (!screen.queryByRole("group", { name: "Restore time" })) {
    await userEvent.click(screen.getByRole("combobox", { name: "Versions" }));
    await userEvent.click(screen.getByRole("option", { name: "Latest archive at or before…" }));
  }
  const picker = await screen.findByRole("group", { name: "Restore time" }, { timeout: 5000 });
  const user = userEvent.setup();
  await user.click(within(picker).getByRole("spinbutton", { name: "Year" }));
  await user.paste(date.replace("T", " "));
};
beforeEach(() => {
  vi.clearAllMocks();
  browserFiles.current = [];
  inspect.mockImplementation(({ selections, fileVersionIds }: { selections: FileSelection[]; fileVersionIds: bigint[] }) =>
    call(SelectionInspectionResult.create({ selections, fileCount: BigInt(selections.length + fileVersionIds.length), totalBytes: 100n })),
  );
  sessionStorage.clear();
  localStorage.clear();
  listFiles.mockImplementation(({ directory }: { directory: { target: { location: { locationId: bigint; path: string } } } }) => {
    const { locationId, path } = directory.target.location;
    const entry = (path: string) =>
      FilesEntry.create({
        name: path.split("/").at(-1),
        path,
        kind: EntryKind.DIRECTORY,
        operations: [FileOperationKind.MKDIR],
        reference: { target: { oneofKind: "location", location: { locationId, path } } },
      });
    return streamListing(
      ListFilesResponse.create({
        directory: entry(path),
        entries: path ? [] : [entry("review"), entry("ignored")],
        breadcrumbs: [entry(""), ...(path ? [entry(path)] : [])],
      }),
    );
  });
  parents.mockReturnValue(call({ parents: [File.create({ id: 1n, name: "Trips" }), File.create({ id: 7n, name: "organized.jpg" })] }));
  getVersion.mockImplementation(({ id }: { id: bigint }) =>
    call({ version: FileVersion.create({ id, fileId: 7n, signature: new Uint8Array([Number(id)]), sizeBytes: 100n }) }),
  );
  listCopies.mockReturnValue(call({ positions: [Position.create({ id: 2n })], hasMore: false }));
  listVersions.mockReturnValue(call({ versions: [FileVersion.create({ id: 20n, fileId: 7n, sizeBytes: 100n })], hasMore: false }));
  list.mockReturnValue(
    call({
      locations: [Location.create({ id: 3n, name: "Recovered files", rootPath: "/restored", restoreTarget: true })],
      hasMore: false,
    }),
  );
  get.mockReturnValue(
    call({
      location: Location.create({ id: 3n, name: "Recovered files", rootPath: "/restored", restoreTarget: true }),
    }),
  );
  create.mockReturnValue(call({ job: { id: 42n } }));
});
describe("Version Restore selection", () => {
  it("does not add a delayed deep-linked version after the route selects another version", async () => {
    let finish!: (value: unknown) => void;
    getVersion.mockImplementation(({ id }: { id: bigint }) =>
      id === 19n
        ? {
            response: new Promise((resolve) => {
              finish = resolve;
            }),
          }
        : call({ version: FileVersion.create({ id, fileId: 7n, sizeBytes: 100n }) }),
    );
    show();
    await waitFor(() => expect(getVersion).toHaveBeenCalledWith({ id: 19n }));
    await userEvent.click(screen.getByRole("button", { name: "Add another version" }));
    await screen.findByText("Selected · 1");
    await act(async () => finish({ version: FileVersion.create({ id: 19n, fileId: 7n, sizeBytes: 100n }) }));
    expect(screen.getByText("Selected · 1")).toBeInTheDocument();
    expect(parents).toHaveBeenCalledTimes(1);
  });

  it("retains a directory root and sends a nested custom version as an override", async () => {
    browserFiles.current = [{ id: "1", name: "Trips", isDir: true }];
    show("/restore");
    await userEvent.click(screen.getByRole("button", { name: "Add Trips" }));
    await userEvent.click(await screen.findByRole("button", { name: "Choose nested file version" }));
    await userEvent.click(await screen.findByRole("button", { name: /Version #20/ }));
    await userEvent.click(screen.getByRole("button", { name: "Choose version" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(await screen.findByText("Selected · 2")).toBeInTheDocument();
    await target();
    await waitFor(() => expect(screen.getByRole("button", { name: "Prepare restore" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Prepare restore" }));
    expect(create).toHaveBeenCalledWith(
      expect.objectContaining({
        spec: expect.objectContaining({
          selections: [expect.objectContaining({ target: { oneofKind: "library", library: { fileId: 1n } }, scope: FileScope.SAVED })],
          fileVersionIds: [20n],
        }),
      }),
    );
  });
  it("keeps automatic file identity and folder roots, resolves against the selected time, and freezes the policy on create", async () => {
    browserFiles.current = [
      { id: "7", name: "organized.jpg" },
      { id: "1", name: "Trips", isDir: true },
    ];
    inspect.mockImplementation((request: EstimateRestoreJobRequest) =>
      call(
        SelectionInspectionResult.create({
          selections: request.selections,
          fileCount: 1n,
          totalBytes: 90n,
          resolvedVersions: [
            {
              fileId: 7n,
              version: { id: request.versionPolicy?.beforeAtNs ? 18n : 19n, fileId: 7n },
              archivedAtNs: request.versionPolicy?.beforeAtNs ?? 1790000000000000000n,
            },
          ],
        }),
      ),
    );
    show("/restore");
    await userEvent.click(screen.getByRole("button", { name: "Add organized.jpg" }));
    await userEvent.click(screen.getByRole("button", { name: "Add Trips" }));
    await target();
    await before("2026-09-01T12:00");
    await waitFor(() => expect(screen.getByRole("button", { name: "Prepare restore" })).toBeEnabled());
    const cutoff = BigInt(new Date("2026-09-01T12:00").getTime()) * 1_000_000n;
    expect(inspect).toHaveBeenLastCalledWith(
      expect.objectContaining({
        fileVersionIds: [],
        versionPolicy: { beforeAtNs: cutoff },
        selections: [
          FileSelection.create({ target: { oneofKind: "library", library: { fileId: 7n } }, scope: FileScope.SAVED }),
          FileSelection.create({ target: { oneofKind: "library", library: { fileId: 1n } }, scope: FileScope.SAVED }),
        ],
      }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Prepare restore" }));
    expect(create).toHaveBeenCalledWith(
      expect.objectContaining({ spec: expect.objectContaining({ fileVersionIds: [], versionPolicy: { beforeAtNs: cutoff }, selections: expect.any(Array) }) }),
    );
  });
  it("keeps explicit versions across time changes and can reset all overrides into a deduplicated File selection", async () => {
    show();
    await screen.findByText("Trips/organized.jpg");
    await userEvent.click(screen.getByRole("button", { name: "Add another version" }));
    await target();
    await before("2026-09-01T12:00");
    await waitFor(() => expect(inspect).toHaveBeenLastCalledWith(expect.objectContaining({ fileVersionIds: [19n, 20n], selections: [] })));
    await userEvent.click(screen.getByRole("button", { name: "Apply current policy to all" }));
    await waitFor(() =>
      expect(inspect).toHaveBeenLastCalledWith(
        expect.objectContaining({
          fileVersionIds: [],
          selections: [FileSelection.create({ target: { oneofKind: "library", library: { fileId: 7n } }, scope: FileScope.SAVED })],
        }),
      ),
    );
    expect(screen.queryByRole("button", { name: "Apply current policy to all" })).not.toBeInTheDocument();
  });
  it("requires explicit skip for unmatched versions, resets that consent on time changes, and never skips missing copies", async () => {
    browserFiles.current = [{ id: "7", name: "organized.jpg" }];
    inspect.mockImplementation((request: EstimateRestoreJobRequest) =>
      call(
        SelectionInspectionResult.create({
          selections: request.selections,
          fileCount: 1n,
          totalBytes: 50n,
          unmatchedVersionCount: request.versionPolicy?.beforeAtNs ? 1n : 0n,
          skippedVersionCount: request.skipUnmatchedVersions ? 1n : 0n,
          missingCopyCount: request.allowDamagedCopies ? 1n : 0n,
          resolvedVersions: [{ fileId: 7n, match: RestoreVersionMatch.AFTER_CUTOFF }],
        }),
      ),
    );
    show("/restore");
    await userEvent.click(screen.getByRole("button", { name: "Add organized.jpg" }));
    await target();
    await before("2026-09-01T12:00");
    await screen.findByText("1 item has no version matching this policy.");
    expect(screen.getByRole("button", { name: "Prepare restore" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Skip 1 item" }));
    await screen.findByText("1 item skipped.");
    expect(screen.getByRole("button", { name: "Prepare restore" })).toBeEnabled();
    await before("2026-09-02T12:00");
    await screen.findByText("1 item has no version matching this policy.");
    expect(screen.getByRole("button", { name: "Prepare restore" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Skip 1 item" }));
    await screen.findByText("1 item skipped.");
    await userEvent.click(screen.getByText("Advanced recovery"));
    await userEvent.click(screen.getByRole("checkbox", { name: "Allow damaged copies" }));
    await screen.findByText("1 item has no usable archived copy.");
    expect(screen.getByRole("button", { name: "Prepare restore" })).toBeDisabled();
  });
  it("does not create an empty restore after explicitly skipping all unmatched files", async () => {
    inspect.mockImplementation((request: EstimateRestoreJobRequest) =>
      call(
        SelectionInspectionResult.create({
          selections: request.selections,
          unmatchedVersionCount: 1n,
          skippedVersionCount: request.skipUnmatchedVersions ? 1n : 0n,
        }),
      ),
    );
    show();
    await target();
    await before("2026-09-01T12:00");
    await userEvent.click(await screen.findByRole("button", { name: "Skip 1 item" }));
    await screen.findByText("1 item skipped.");
    expect(screen.getByRole("button", { name: "Prepare restore" })).toBeDisabled();
    expect(create).not.toHaveBeenCalled();
  });
  it("discards an older policy response and remembers the selected policy with the waitlist", async () => {
    let resolveOld!: (value: SelectionInspectionResult) => void;
    const oldCutoff = BigInt(new Date("2026-09-01T12:00").getTime()) * 1_000_000n;
    inspect.mockImplementation((request: EstimateRestoreJobRequest) => {
      if (request.versionPolicy?.beforeAtNs === oldCutoff)
        return {
          response: new Promise<SelectionInspectionResult>((resolve) => {
            resolveOld = resolve;
          }),
        };
      return call(SelectionInspectionResult.create({ selections: request.selections, fileCount: 1n, totalBytes: 100n }));
    });
    const page = show();
    await target();
    await before("2026-09-01T12:00");
    await waitFor(() => expect(resolveOld).toBeTypeOf("function"));
    await before("2026-09-02T12:00");
    await waitFor(() => expect(screen.getByRole("button", { name: "Prepare restore" })).toBeEnabled());
    await act(async () => resolveOld(SelectionInspectionResult.create({ missingCopyCount: 1n, fileCount: 1n })));
    expect(screen.queryByText("1 item has no usable archived copy.")).not.toBeInTheDocument();
    page.unmount();
    show("/restore");
    await userEvent.click(screen.getByRole("button", { name: "Restore…" }));
    const picker = await screen.findByRole("group", { name: "Restore time" });
    expect(within(picker).getByRole("spinbutton", { name: "Day" })).toHaveTextContent("02");
    expect(within(picker).getByRole("spinbutton", { name: "Hours" })).toHaveTextContent("12");
  });
  it("warns about ignored outputs after confirming a target", async () => {
    inspect.mockImplementation(({ selections, destination }: { selections: FileSelection[]; destination?: unknown }) =>
      call(SelectionInspectionResult.create({ selections, fileCount: 1n, ignoredOutputCount: destination ? 1n : 0n })),
    );
    show();
    await target();
    expect(await screen.findByText(/1 outputs match Ignore rules/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Prepare restore" })).toBeEnabled();
  });
  it("can deliberately include damaged copies and re-estimates eligibility", async () => {
    inspect.mockImplementation(({ selections, allowDamagedCopies }: { selections: FileSelection[]; allowDamagedCopies: boolean }) =>
      call(SelectionInspectionResult.create({ selections, fileCount: 1n, missingCopyCount: allowDamagedCopies ? 0n : 1n })),
    );
    show();
    await target();
    await screen.findByText("1 item has no usable archived copy.");
    expect(screen.getByRole("button", { name: "Prepare restore" })).toBeDisabled();
    await userEvent.click(screen.getByText("Advanced recovery"));
    await userEvent.click(screen.getByRole("checkbox", { name: "Allow damaged copies" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Prepare restore" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Prepare restore" }));
    expect(create).toHaveBeenCalledWith(expect.objectContaining({ spec: expect.objectContaining({ allowDamagedCopies: true }) }));
  });
  it("submits an explicit version with a restore-only destination that needs no scan", async () => {
    show();
    await screen.findByText("Trips/organized.jpg");
    expect(screen.queryByRole("button", { name: "Prepare restore" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restore…" })).toBeEnabled();
    await target("review");
    await waitFor(() => expect(screen.getByRole("button", { name: "Prepare restore" })).toBeEnabled());
    await userEvent.click(await screen.findByRole("button", { name: "Prepare restore" }));
    expect(await screen.findByText("Job 42")).toBeInTheDocument();
    expect(create).toHaveBeenCalledWith(
      expect.objectContaining({
        spec: expect.objectContaining({ fileVersionIds: [19n], destination: expect.objectContaining({ locationId: 3n, path: "review" }) }),
      }),
    );
  });
  it("keeps multiple versions of one File and lets the backend reserve non-conflicting outputs", async () => {
    show();
    await screen.findByText("Trips/organized.jpg");
    await userEvent.click(screen.getByRole("button", { name: "Add another version" }));
    expect(await screen.findByText("Selected · 2")).toBeInTheDocument();
    expect(screen.getAllByText("Trips/organized.jpg")).toHaveLength(2);
    await target();
    await waitFor(() => expect(screen.getByRole("button", { name: "Prepare restore" })).toBeEnabled());
    await userEvent.click(await screen.findByRole("button", { name: "Prepare restore" }));
    await waitFor(() => expect(create).toHaveBeenCalledWith(expect.objectContaining({ spec: expect.objectContaining({ fileVersionIds: [19n, 20n] }) })));
  });
  it("keeps a version with no remaining copy visible but blocks creation", async () => {
    listCopies.mockReturnValue(call({ positions: [], hasMore: false }));
    inspect.mockImplementation(() => call(SelectionInspectionResult.create({ fileCount: 1n, missingCopyCount: 1n })));
    show();
    await target();
    await screen.findByText("1 item has no usable archived copy.");
    expect(await screen.findByRole("button", { name: "Prepare restore" })).toBeDisabled();
    expect(create).not.toHaveBeenCalled();
  });
  it("keeps the confirmed target and version choice when configuration is cancelled", async () => {
    show();
    expect(screen.queryByRole("heading", { name: /New restore/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Locations" })).not.toBeInTheDocument();
    await screen.findByText("Trips/organized.jpg");
    await userEvent.click(screen.getByRole("button", { name: "Change version" }));
    await userEvent.click(await screen.findByRole("button", { name: /Version #20/ }));
    await userEvent.click(screen.getByRole("button", { name: "Choose version" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await target("review");
    expect(create).not.toHaveBeenCalled();
    await userEvent.click(within(screen.getByRole("dialog", { name: "Restore" })).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByText("Selected · 1")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Restore…" }));
    expect(screen.getByRole("button", { name: "Choose restore target" })).toHaveTextContent("Recovered files / review");
    await waitFor(() => expect(screen.getByRole("button", { name: "Prepare restore" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Prepare restore" }));
    expect(create).toHaveBeenCalledWith(
      expect.objectContaining({
        spec: expect.objectContaining({ fileVersionIds: [20n], destination: expect.objectContaining({ locationId: 3n, path: "review" }) }),
      }),
    );
  });
});

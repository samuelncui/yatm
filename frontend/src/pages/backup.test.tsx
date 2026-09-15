import type { PropsWithChildren, ReactNode } from "react";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Routes, Route, useParams } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { File, FileScope, InspectSelectionReply, PreviewPolicy, type FileSelection } from "@/entity";

const { archiveCreate, parents, inspect } = vi.hoisted(() => ({
  archiveCreate: vi.fn(),
  parents: vi.fn(),
  inspect: vi.fn(),
}));
vi.mock("@/api", async (original) => ({
  ...(await original<typeof import("@/api")>()),
  archiveJobCli: { create: archiveCreate },
  cli: { fileListParents: parents },
  fileCatalogCli: { inspectSelection: inspect },
}));
vi.mock("@/pages/file", () => ({
  useFileBrowser: () => ({
    refresh: async () => {},
    scope: FileScope.ALL,
    files: [],
    selector: <button>Library</button>,
    browserProps: { files: [], onFileAction: vi.fn() },
  }),
}));
vi.mock("@samuelncui/chonky", async (original) => {
  const actual = await original<typeof import("@samuelncui/chonky")>();
  const { forwardRef } = await import("react");
  return {
    ...actual,
    FileBrowser: forwardRef(({ children, onFileAction }: PropsWithChildren<{ onFileAction: (action: unknown) => void }>, _ref) => (
      <div>
        {children}
        <button
          onClick={() =>
            onFileAction({
              id: "add_job_selection",
              state: {
                selectedFilesForAction: [
                  { id: "7", name: "photo.jpg" },
                  { id: "8", name: "notes.md" },
                ],
              },
            })
          }
        >
          Add two files
        </button>
        <button onClick={() => onFileAction({ id: "add_job_selection", state: { selectedFilesForAction: [{ id: "9", name: "Plans", isDir: true }] } })}>
          Add another directory
        </button>
        <button
          onClick={() =>
            onFileAction({
              id: "move_files",
              payload: { sourceInstanceId: "select-archive", destination: { id: "waitlist:archive" }, files: [{ id: "7", name: "photo.jpg" }] },
            })
          }
        >
          Drag to waitlist
        </button>
        <button
          onClick={() =>
            onFileAction({
              id: "move_files",
              payload: { sourceInstanceId: "select-archive", destination: { id: "some-physical-directory" }, files: [{ id: "8", name: "notes.md" }] },
            })
          }
        >
          Drag to directory
        </button>
      </div>
    )),
    FileNavbar: () => null,
    FileToolbar: () => null,
    FileList: () => null,
    FileContextMenu: () => null,
  };
});
import { BackupBrowser } from "./backup";
vi.mock("@/components/selection-waitlist", async (original) => ({
  ...(await original<typeof import("@/components/selection-waitlist")>()),
  SelectionWaitlist: ({
    entries,
    onRemove,
    footer,
  }: {
    entries: import("@/components/selection-waitlist").SelectionEntry[];
    onRemove: (keys: Set<string>) => void;
    footer?: ReactNode;
  }) => (
    <div>
      <span>Selected · {entries.length}</span>
      {entries.map((entry) => (
        <div key={entry.key}>
          <span>{entry.path}</span>
          <button onClick={() => onRemove(new Set([entry.key]))}>Remove</button>
        </div>
      ))}
      {footer}
    </div>
  ),
}));
const call = (value: unknown) => ({ response: Promise.resolve(value) });
const Destination = () => <div>Job {useParams().id}</div>;
const show = () =>
  render(
    <MemoryRouter initialEntries={["/backup"]}>
      <Routes>
        <Route path="/backup" element={<BackupBrowser />} />
        <Route path="/jobs/:id" element={<Destination />} />
      </Routes>
    </MemoryRouter>,
  );
const configure = () => userEvent.click(screen.getByRole("button", { name: "Backup…" }));
beforeEach(() => {
  vi.clearAllMocks();
  inspect.mockImplementation(({ selections }: { selections: FileSelection[] }) =>
    call(InspectSelectionReply.create({ selections, files: BigInt(selections.length), bytes: 100n })),
  );
  sessionStorage.clear();
  archiveCreate.mockReturnValue(call({ job: { id: 42n } }));
  parents.mockImplementation(({ id }: { id: bigint }) =>
    call({
      parents: [
        File.create({ id: 3n, name: id === 9n ? "Work" : "Trips" }),
        File.create({ id, name: id === 7n ? "photo.jpg" : id === 8n ? "notes.md" : "Plans" }),
      ],
    }),
  );
});
describe("Unified Backup selection", () => {
  it("accepts the shared drag completion only for its waitlist and never turns it into a directory operation", async () => {
    show();
    await userEvent.click(screen.getByRole("button", { name: "Drag to directory" }));
    expect(screen.getByText("Selected · 0")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Drag to waitlist" }));
    expect(await screen.findByText("Selected · 1")).toBeInTheDocument();
    await configure();
    await waitFor(() => expect(screen.getByRole("button", { name: "Prepare backup" })).toBeEnabled());
    expect(inspect).toHaveBeenCalledOnce();
    expect(archiveCreate).not.toHaveBeenCalled();
  });
  it("reports unknown sizes separately from signatures and ignores an obsolete estimate", async () => {
    let resolve!: (result: InspectSelectionReply) => void;
    inspect.mockImplementationOnce(() => ({
      response: new Promise<InspectSelectionReply>((done) => {
        resolve = done;
      }),
    }));
    inspect.mockImplementation(({ selections }: { selections: FileSelection[] }) =>
      call(InspectSelectionReply.create({ selections, files: 3n, bytes: 10n, unknownSizeFiles: 1n })),
    );
    show();
    await userEvent.click(screen.getByRole("button", { name: "Add two files" }));
    await waitFor(() => expect(inspect).toHaveBeenCalledOnce());
    await userEvent.click(screen.getByRole("button", { name: "Add another directory" }));
    resolve(InspectSelectionReply.create({ files: 99n, missingOriginals: 99n }));
    await configure();
    expect(await screen.findByText(/3 files · 10 B known · 1 sizes unknown/)).toBeInTheDocument();
    expect(screen.queryByText(/99 files have no usable/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Prepare backup" })).toBeEnabled();
  });
  it("allows usable originals but invalidates the review when the selection changes", async () => {
    inspect.mockImplementation(({ selections }: { selections: FileSelection[] }) => call(InspectSelectionReply.create({ selections, files: 2n })));
    show();
    await userEvent.click(screen.getByRole("button", { name: "Add two files" }));
    await screen.findByText("Selected · 2");
    await configure();
    await waitFor(() => expect(screen.getByRole("button", { name: "Prepare backup" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await userEvent.click(screen.getAllByRole("button", { name: "Remove" })[0]);
    await configure();
    expect(screen.getByRole("button", { name: "Prepare backup" })).toBeDisabled();
  });
  it("blocks known missing originals after reviewing a directory", async () => {
    inspect.mockReturnValue(call(InspectSelectionReply.create({ files: 3n, missingOriginals: 1n })));
    show();
    await userEvent.click(screen.getByRole("button", { name: "Add another directory" }));
    await screen.findByText("Selected · 1");
    await configure();
    expect(await screen.findByText(/1 file has no usable original/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Prepare backup" })).toBeDisabled();
    expect(archiveCreate).not.toHaveBeenCalled();
  });
  it("keeps the selection summary compact without hiding paths or warnings", async () => {
    inspect.mockReturnValue(
      call(
        InspectSelectionReply.create({
          files: 22n,
          bytes: 100n,
          unknownSizeFiles: 1n,
          missingOriginals: 1n,
        }),
      ),
    );
    show();
    expect(screen.queryByText(/Drag files or folders here/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Add two files" }));
    await configure();
    expect(await screen.findByText(/22 files · 100 B known · 1 sizes unknown/)).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("1 file has no usable original");
    expect(screen.getByText("Trips/photo.jpg")).toBeInTheDocument();
    expect(screen.getByText("Trips/notes.md")).toBeInTheDocument();
    expect(screen.queryByText(/Output paths|Estimate only|hashed during preparation/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Prepare backup" })).toBeDisabled();
  });
  it("accumulates multiple files and directories across navigation and deduplicates repeated additions", async () => {
    show();
    await userEvent.click(screen.getByRole("button", { name: "Add two files" }));
    await screen.findByText("Trips/photo.jpg");
    await userEvent.click(screen.getByRole("button", { name: "Add another directory" }));
    await userEvent.click(screen.getByRole("button", { name: "Add two files" }));
    expect(await screen.findByText("Selected · 3")).toBeInTheDocument();
    await configure();
    await waitFor(() => expect(screen.getByRole("button", { name: "Prepare backup" })).toBeEnabled());
    await userEvent.click(await screen.findByRole("button", { name: "Prepare backup" }));
    expect(await screen.findByText("Job 42")).toBeInTheDocument();
    expect(archiveCreate).toHaveBeenCalledWith(
      expect.objectContaining({
        spec: expect.objectContaining({
          sources: [],
          fileIds: [],
          selections: [
            { target: { oneofKind: "library", library: { fileId: 7n } }, scope: FileScope.ALL },
            { target: { oneofKind: "library", library: { fileId: 8n } }, scope: FileScope.ALL },
            { target: { oneofKind: "library", library: { fileId: 9n } }, scope: FileScope.ALL },
          ],
        }),
      }),
    );
  });
  it("preserves selections when leaving and returning to creation", async () => {
    const page = show();
    await userEvent.click(screen.getByRole("button", { name: "Add two files" }));
    await screen.findByText("Selected · 2");
    page.unmount();
    show();
    expect(screen.getByText("Selected · 2")).toBeInTheDocument();
    expect(screen.getByText("Trips/notes.md")).toBeInTheDocument();
  });
  it("uses one Preview policy and never rehashes when Preview is disabled", async () => {
    show();
    await userEvent.click(screen.getByRole("button", { name: "Add two files" }));
    await screen.findByText("Selected · 2");
    await configure();
    expect(screen.queryByRole("checkbox", { name: "Force rehash" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("combobox", { name: "Previews" }));
    await userEvent.click(screen.getByRole("option", { name: "Regenerate all" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Force rehash" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Previews" }));
    await userEvent.click(screen.getByRole("option", { name: "Don’t generate" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Prepare backup" })).toBeEnabled());
    await userEvent.click(await screen.findByRole("button", { name: "Prepare backup" }));
    await waitFor(() => expect(archiveCreate).toHaveBeenCalledWith(expect.objectContaining({ previewPolicy: PreviewPolicy.PREVIEW_NONE, forceRehash: false })));
  });
  it("opens configuration without creating a Job and keeps the list and options after Cancel", async () => {
    show();
    expect(screen.queryByRole("heading", { name: /New backup/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Locations" })).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: "Previews" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Backup…" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Add two files" }));
    await screen.findByText("Selected · 2");
    await configure();
    const dialog = screen.getByRole("dialog", { name: "Backup" });
    expect(archiveCreate).not.toHaveBeenCalled();
    await userEvent.click(within(dialog).getByRole("combobox", { name: "Previews" }));
    await userEvent.click(screen.getByRole("option", { name: "Regenerate all" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByText("Selected · 2")).toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: "Previews" })).not.toBeInTheDocument();
    await configure();
    expect(screen.getByRole("combobox", { name: "Previews" })).toHaveTextContent("Regenerate all");
    expect(archiveCreate).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByRole("button", { name: "Prepare backup" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Prepare backup" }));
    await waitFor(() => expect(archiveCreate).toHaveBeenCalledWith(expect.objectContaining({ previewPolicy: PreviewPolicy.PREVIEW_REGENERATE_ALL })));
  });
  it("keeps creation failures in the modal and allows only its final action to submit", async () => {
    archiveCreate.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Archive is busy")) }));
    show();
    await userEvent.click(screen.getByRole("button", { name: "Add two files" }));
    await screen.findByText("Selected · 2");
    await configure();
    expect(archiveCreate).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByRole("button", { name: "Prepare backup" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Prepare backup" }));
    const dialog = screen.getByRole("dialog", { name: "Backup" });
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("Archive is busy");
    expect(archiveCreate).toHaveBeenCalledOnce();
    expect(sessionStorage.getItem("job-selection:archive")).toContain("photo.jpg");
    await userEvent.click(within(dialog).getByRole("button", { name: "Prepare backup" }));
    expect(await screen.findByText("Job 42")).toBeInTheDocument();
    expect(archiveCreate).toHaveBeenCalledTimes(2);
  });
  it("does not dismiss or submit again while preparation is running", async () => {
    let finish!: (value: { job: { id: bigint } }) => void;
    archiveCreate.mockImplementationOnce(() => ({
      response: new Promise<{ job: { id: bigint } }>((resolve) => {
        finish = resolve;
      }),
    }));
    show();
    await userEvent.click(screen.getByRole("button", { name: "Add two files" }));
    await screen.findByText("Selected · 2");
    await configure();
    await waitFor(() => expect(screen.getByRole("button", { name: "Prepare backup" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Prepare backup" }));
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Preparing…" })).toBeDisabled();
    await userEvent.keyboard("{Escape}");
    expect(screen.getByRole("dialog", { name: "Backup" })).toBeInTheDocument();
    expect(archiveCreate).toHaveBeenCalledOnce();
    finish({ job: { id: 42n } });
    expect(await screen.findByText("Job 42")).toBeInTheDocument();
  });
  it("shows an estimate failure inside configuration and retries without creating a Job", async () => {
    inspect.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Could not inspect originals")) }));
    show();
    await userEvent.click(screen.getByRole("button", { name: "Add two files" }));
    await screen.findByText("Selected · 2");
    await configure();
    expect(await within(screen.getByRole("dialog", { name: "Backup" })).findByRole("alert")).toHaveTextContent("Could not inspect originals");
    expect(screen.getByRole("button", { name: "Prepare backup" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Retry estimate" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Prepare backup" })).toBeEnabled());
    expect(archiveCreate).not.toHaveBeenCalled();
  });
});

import { act, screen, waitFor, within } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, expect, it, vi } from "vitest";
import {
  EntryKind,
  FilesDetail,
  FileVersion,
  OriginalAvailability,
  PreviewAvailability,
  Position,
  PositionHealth,
  FileOperationRef,
  FileOperationKind,
  FileScope,
  FileSelection,
  Media,
  MediaKind,
  MediaAccess,
} from "@/entity";
import { ContentCopies, FileContent } from "./file-content";
import { contentTime } from "./content-status";
import { saveSelectionEntries, loadSelectionEntries } from "./selection-waitlist-state";
const { preview, versions, copies, media, duplicates, removeVersion, updateMetadata, listTags, report, success, archiveCreate, restoreCreate } = vi.hoisted(
  () => ({
    preview: vi.fn(),
    removeVersion: vi.fn(),
    report: vi.fn(),
    versions: vi.fn(),
    copies: vi.fn(),
    media: vi.fn(),
    duplicates: vi.fn(),
    updateMetadata: vi.fn(),
    listTags: vi.fn(),
    success: vi.fn(),
    archiveCreate: vi.fn(),
    restoreCreate: vi.fn(),
  }),
);
vi.mock("@/api", () => ({
  fileBase: "/files",
  filesCli: { removeVersion, updateMetadata, listVersions: versions, listCopies: copies, listDuplicates: duplicates },
  previewCli: { get: preview },
  mediaCli: { list: media },
  cli: { listTags },
  archiveJobCli: { create: archiveCreate },
  restoreJobCli: { create: restoreCreate },
}));
vi.mock("react-toastify", () => ({ toast: { error: report, success } }));
vi.mock("./file-metadata-dialog", () => ({ FileMetadataDialog: () => null }));
vi.mock("./relocate-original", () => ({
  RelocateOriginalDialog: ({ file }: { file: { id: bigint; name: string } }) => (
    <div role="dialog" aria-label="Locate original" data-file-id={String(file.id)}>
      {file.name}
    </div>
  ),
}));
const call = (value: unknown) => ({ response: Promise.resolve(value) });
const saved = FileVersion.create({ id: 2n, fileId: 7n, sizeBytes: 12n, signature: new Uint8Array([1]) });
const live = FileOperationRef.create({ target: { oneofKind: "location", location: { locationId: 4n, path: "image.jpg" } } });
const currentSignature = new Uint8Array([9, 9]);
const detail = () =>
  FilesDetail.create({
    entry: {
      reference: { target: { oneofKind: "fileId", fileId: 7n } },
      name: "image.jpg",
      kind: EntryKind.FILE,
      sizeBytes: 12n,
      status: { original: OriginalAvailability.PRESENT },
      operations: [FileOperationKind.ARCHIVE, FileOperationKind.UPDATE_METADATA],
    },
    organization: { tags: ["keep"], note: "A note" },
    original: { sourceName: "Pictures", path: "image.jpg", reference: live },
    contentReference: live,
    contentSignature: currentSignature,
  });
beforeEach(() => {
  vi.clearAllMocks();
  sessionStorage.clear();
  preview.mockReturnValue(call({ availability: PreviewAvailability.NOT_GENERATED, assets: [] }));
  versions.mockReturnValue(call({ versions: [saved], hasMore: false }));
  copies.mockReturnValue(call({ positions: [], hasMore: false }));
  media.mockReturnValue(call({ media: [] }));
  updateMetadata.mockReturnValue(call({}));
  listTags.mockReturnValue(call({ tags: [], nextCursor: "" }));
});
const refresh = async () => {};
it.each([
  { source: "Library", reference: FileOperationRef.create({ target: { oneofKind: "fileId", fileId: 7n } }), associatedFileId: undefined, expected: 7n },
  { source: "associated Location", reference: live, associatedFileId: 9n, expected: 9n },
  { source: "unadmitted Location", reference: live, associatedFileId: undefined, expected: undefined },
])("offers Locate original only for the existing Library File in $source", async ({ reference, associatedFileId, expected }) => {
  const value = detail();
  value.entry!.reference = reference;
  value.entry!.associatedFileId = associatedFileId;
  render(<FileContent detail={value} onRefresh={refresh} />, { wrapper: MemoryRouter });
  if (expected === undefined) {
    expect(screen.queryByRole("button", { name: "Locate original" })).not.toBeInTheDocument();
    return;
  }
  await userEvent.click(screen.getByRole("button", { name: "Locate original" }));
  expect(screen.getByRole("dialog", { name: "Locate original" })).toHaveAttribute("data-file-id", String(expected));
});

it("retries Preview without loading history or copies", async () => {
  preview.mockReturnValueOnce({ response: Promise.reject(new Error("Preview offline")) });
  render(<FileContent detail={detail()} onRefresh={refresh} />, { wrapper: MemoryRouter });
  await userEvent.click(await screen.findByRole("button", { name: "Retry Preview" }));
  await waitFor(() => expect(screen.queryByText("Preview offline")).not.toBeInTheDocument());
  expect(preview).toHaveBeenCalledTimes(2);
  expect(versions).not.toHaveBeenCalled();
  expect(copies).not.toHaveBeenCalled();
});

it("clears a previous Preview error when the current original's signature becomes unknown", async () => {
  preview.mockReturnValueOnce({ response: Promise.reject(new Error("Preview offline")) });
  const { rerender } = render(<FileContent detail={detail()} onRefresh={refresh} />, { wrapper: MemoryRouter });
  await screen.findByRole("button", { name: "Retry Preview" });

  const unknown = detail();
  unknown.contentSignature = new Uint8Array();
  rerender(<FileContent detail={unknown} onRefresh={refresh} />);

  await waitFor(() => expect(screen.queryByText("Preview offline")).not.toBeInTheDocument());
  expect(screen.queryByRole("button", { name: "Retry Preview" })).not.toBeInTheDocument();
  expect(screen.getByText("Preview not available")).toBeInTheDocument();
  expect(preview).toHaveBeenCalledOnce();
  expect(versions).not.toHaveBeenCalled();
});

it("paginates matching Library entries by their references without Location association IDs or detail reads", async () => {
  const entry = (fileId: bigint) => ({ reference: { target: { oneofKind: "fileId", fileId } }, name: `file-${fileId}`, path: `Folder/file-${fileId}` });
  duplicates.mockImplementation(({ afterFileId }) => call({ entries: afterFileId ? [entry(9n)] : [entry(7n), entry(8n)], hasMore: !afterFileId }));
  render(<ContentCopies signature={new Uint8Array([1])} fileID={7n} />);
  await userEvent.click(await screen.findByRole("button", { name: "Find matching files" }));
  expect(await screen.findByRole("link", { name: /file-8/ })).toHaveAttribute("href", "/file?file=8");
  expect(screen.queryByRole("link", { name: /file-7/ })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Load more matching files" }));
  expect(await screen.findByRole("link", { name: /file-9/ })).toHaveAttribute("href", "/file?file=9");
  expect(duplicates).toHaveBeenLastCalledWith({ signature: new Uint8Array([1]), afterFileId: 8n, limit: 20 });
});

it("keeps the file tabs fixed and loads versions and their copies when opened", async () => {
  render(<FileContent detail={detail()} onRefresh={refresh} />, { wrapper: MemoryRouter });
  expect(screen.getByTitle(/Local file present/)).toBeInTheDocument();
  expect(screen.queryByRole("region", { name: "Archive status" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Locate original" })).toBeEnabled();
  expect(screen.getByRole("link", { name: "image.jpg" })).toHaveAttribute("href", "/file?location=4&reveal=image.jpg");
  expect(versions).not.toHaveBeenCalled();
  expect(copies).not.toHaveBeenCalled();
  await waitFor(() => expect(preview).toHaveBeenCalledOnce());
  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  expect(await screen.findByRole("region", { name: "Archived copies" })).toBeInTheDocument();
  expect(screen.queryByRole("link", { name: "Restore this version" })).not.toBeInTheDocument();
  expect(versions).toHaveBeenCalledWith({ fileId: 7n, afterId: 0n, limit: 20 }, expect.objectContaining({ abort: expect.any(AbortSignal) }));
  expect(screen.getAllByText("Permission")).toHaveLength(2);
  await waitFor(() => expect(copies).toHaveBeenCalledWith({ signature: saved.signature, afterId: 0n, limit: 20 }));
  expect(copies).toHaveBeenCalledOnce();
  await userEvent.click(screen.getByRole("tab", { name: "Overview" }));
  expect(screen.getByRole("tab", { name: "Saved versions" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  expect(versions).toHaveBeenCalledOnce();
});
it("adds an eligible current original to the Archive list without leaving details or creating a Job", async () => {
  render(<FileContent detail={detail()} onRefresh={refresh} />, { wrapper: MemoryRouter });

  await userEvent.click(screen.getByRole("button", { name: "Add to Archive list" }));

  expect(screen.getByRole("heading", { name: "image.jpg" })).toBeInTheDocument();
  expect(screen.getByText("1 item added to the Archive list.")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "View list" })).toBeInTheDocument();
  expect(loadSelectionEntries("archive")).toHaveLength(1);
  expect(loadSelectionEntries("archive")[0].selection?.target).toEqual({ oneofKind: "library", library: { fileId: 7n } });
  expect(archiveCreate).not.toHaveBeenCalled();
  expect(restoreCreate).not.toHaveBeenCalled();

  await userEvent.click(screen.getByRole("button", { name: "Add to Archive list" }));
  expect(screen.getByText("This item is already in the Archive list.")).toBeInTheDocument();
  expect(loadSelectionEntries("archive")).toHaveLength(1);
});
it("omits Archive for a saved-only regular File while keeping its saved version available for Restore", async () => {
  const value = detail();
  value.entry!.status!.original = OriginalAvailability.UNLINKED;
  value.entry!.operations = [FileOperationKind.ARCHIVE, FileOperationKind.UPDATE_METADATA];
  value.original = undefined;
  value.contentReference = undefined;
  value.contentSignature = new Uint8Array();
  render(<FileContent detail={value} onRefresh={refresh} />, { wrapper: MemoryRouter });

  expect(value.entry!.operations).toContain(FileOperationKind.ARCHIVE);
  expect(screen.getByText("Saved versions only")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Add to Archive list" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Locate original" })).toBeEnabled();
  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  expect(await screen.findByRole("button", { name: "Add to Restore list" })).toBeInTheDocument();
  expect(loadSelectionEntries("archive")).toEqual([]);
});
it("does not offer Archive for a Library File without an original when its status is stale", () => {
  const value = detail();
  value.entry!.status!.original = OriginalAvailability.UNCHECKED;
  value.original = undefined;
  value.contentReference = undefined;
  render(<FileContent detail={value} onRefresh={refresh} />, { wrapper: MemoryRouter });

  expect(value.entry!.operations).toContain(FileOperationKind.ARCHIVE);
  expect(screen.queryByRole("button", { name: "Add to Archive list" })).not.toBeInTheDocument();
});
it("hides Archive when the operation is absent even if an original remains associated", () => {
  const value = detail();
  value.entry!.operations = [FileOperationKind.UPDATE_METADATA];
  render(<FileContent detail={value} onRefresh={refresh} />, { wrapper: MemoryRouter });

  expect(value.original).toBeDefined();
  expect(screen.queryByRole("button", { name: "Add to Archive list" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Locate original" })).toBeEnabled();
});
it("offers a directory root for Archive without requiring an original association or descendant lookup", async () => {
  const value = FilesDetail.create({
    entry: {
      reference: { target: { oneofKind: "fileId", fileId: 8n } },
      name: "Folder",
      kind: EntryKind.DIRECTORY,
      operations: [FileOperationKind.ARCHIVE],
    },
    organization: { path: "Folder" },
  });
  render(<FileContent detail={value} onRefresh={refresh} />, { wrapper: MemoryRouter });

  const archive = screen.getByRole("button", { name: "Add to Archive list" });
  expect(value.original).toBeUndefined();
  expect(archive).toBeEnabled();
  await userEvent.click(archive);
  expect(loadSelectionEntries("archive")).toMatchObject([
    { isDir: true, selection: { target: { oneofKind: "library", library: { fileId: 8n } }, scope: FileScope.DEFAULT } },
  ]);
  expect(versions).not.toHaveBeenCalled();
  expect(copies).not.toHaveBeenCalled();
});
it("puts each list action first in the bottom row of its detail section", async () => {
  render(<FileContent detail={detail()} onRefresh={refresh} />, { wrapper: MemoryRouter });
  const overview = screen.getByRole("tabpanel", { name: "Overview" });
  const archiveActions = screen.getByRole("region", { name: "File actions" });
  expect(overview.lastElementChild).toBe(archiveActions);
  expect(within(archiveActions).getAllByRole("button")[0]).toHaveTextContent("Add to Archive list");

  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  const versionsPanel = await screen.findByRole("tabpanel", { name: "Saved versions" });
  await within(versionsPanel).findByRole("region", { name: "Selected saved version" });
  const restoreActions = versionsPanel.querySelector(".file-detail-version-actions");
  expect(versionsPanel.lastElementChild).toBe(restoreActions);
  expect(
    within(restoreActions as HTMLElement)
      .getAllByRole("button")
      .map((button) => button.textContent),
  ).toEqual(["Add to Restore list", "Remove version"]);
});
it("adds the selected FileVersion to Restore and replaces that File's automatic selection", async () => {
  const selection = FileSelection.create({ target: { oneofKind: "library", library: { fileId: 7n } }, scope: FileScope.SAVED });
  saveSelectionEntries("restore", [
    {
      key: FileSelection.toJsonString(selection),
      name: "image.jpg",
      path: "image.jpg",
      fileID: "7",
      selection,
    },
  ]);
  render(<FileContent detail={detail()} onRefresh={refresh} />, { wrapper: MemoryRouter });

  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  await userEvent.click(await screen.findByRole("button", { name: "Add to Restore list" }));

  const entries = loadSelectionEntries("restore");
  expect(entries).toHaveLength(1);
  expect(entries[0].selection).toBeUndefined();
  expect(entries[0].version?.id).toBe(2n);
  expect(entries[0].version?.fileId).toBe(7n);
  expect(screen.getByText(/automatic selection was replaced by the explicit version/)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "View list" })).toBeInTheDocument();
  expect(archiveCreate).not.toHaveBeenCalled();
  expect(restoreCreate).not.toHaveBeenCalled();
});
it("explains when no saved version can be added to Restore", async () => {
  versions.mockReturnValue(call({ versions: [], hasMore: false }));
  render(<FileContent detail={detail()} onRefresh={refresh} />, { wrapper: MemoryRouter });

  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  expect(await screen.findByText("No saved versions are available to add to the Restore list.")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Add to Restore list" })).not.toBeInTheDocument();
});
it("edits metadata in place, keeps its draft across tabs, and enables Save only for changes", async () => {
  const reload = vi.fn().mockResolvedValue(undefined);
  render(<FileContent detail={detail()} onRefresh={reload} />, { wrapper: MemoryRouter });
  const save = screen.getByRole("button", { name: "Save changes" });
  expect(save).toBeDisabled();
  await userEvent.clear(screen.getByRole("textbox", { name: "Note" }));
  await userEvent.type(screen.getByRole("textbox", { name: "Note" }), "Updated note");
  expect(save).toBeEnabled();
  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  await userEvent.click(screen.getByRole("tab", { name: "Overview" }));
  expect(screen.getByRole("textbox", { name: "Note" })).toHaveValue("Updated note");
  await userEvent.click(save);
  await waitFor(() =>
    expect(updateMetadata).toHaveBeenCalledWith({ references: [detail().entry!.reference], addTags: [], removeTags: [], note: "Updated note" }),
  );
  expect(reload).toHaveBeenCalledOnce();
  expect(save).toBeDisabled();
  expect(success).toHaveBeenCalledWith("File metadata updated");
});
it("offers a new Tag in server search results and clears the query after adding it", async () => {
  render(<FileContent detail={detail()} onRefresh={refresh} />, { wrapper: MemoryRouter });
  const tags = screen.getByRole("combobox", { name: "Tags" });
  await userEvent.type(tags, "brand-new");
  await userEvent.click(await screen.findByRole("option", { name: "Add tag “brand-new”" }));
  expect(tags).toHaveValue("");
  expect(screen.getByRole("button", { name: "brand-new" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await waitFor(() =>
    expect(updateMetadata).toHaveBeenCalledWith({ references: [detail().entry!.reference], addTags: ["brand-new"], removeTags: [], note: undefined }),
  );
});
it("cancels unsaved metadata edits without a request", async () => {
  render(<FileContent detail={detail()} onRefresh={refresh} />, { wrapper: MemoryRouter });
  await userEvent.clear(screen.getByRole("textbox", { name: "Note" }));
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(screen.getByRole("textbox", { name: "Note" })).toHaveValue("A note");
  expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
  expect(updateMetadata).not.toHaveBeenCalled();
});
it("uses a recorded last archive date when the first archive date is absent", async () => {
  versions.mockReturnValue(
    call({ versions: [FileVersion.create({ ...saved, firstArchivedAtNs: undefined, lastArchivedAtNs: 1788825600000000000n })], hasMore: false }),
  );
  render(<FileContent detail={detail()} onRefresh={refresh} />, { wrapper: MemoryRouter });
  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  expect(await screen.findByText(/Last archived/)).toBeInTheDocument();
  expect(screen.queryByText("Archive date not recorded")).not.toBeInTheDocument();
});
it("uses only FileVersion archive dates for display and the default Restore selection", async () => {
  const dated = FileVersion.create({ ...saved, id: 3n, signature: new Uint8Array([3]), lastArchivedAtNs: 1750000000000000000n });
  const unknown = FileVersion.create({ ...saved, id: 4n, signature: new Uint8Array([4]) });
  versions.mockReturnValue(call({ versions: [saved, dated, unknown], hasMore: false }));
  copies.mockReturnValue(call({ positions: [Position.create({ id: 32n, writtenAtNs: 1950000000000000000n })], hasMore: false }));
  render(<FileContent detail={detail()} onRefresh={refresh} />, { wrapper: MemoryRouter });
  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  const list = await screen.findByLabelText("Saved content versions");
  await waitFor(() => {
    const choices = within(list).getAllByRole("button");
    expect(choices[0]).toHaveTextContent(contentTime(dated.lastArchivedAtNs));
    expect(choices[0]).toHaveAttribute("aria-pressed", "true");
    expect(choices[1]).toHaveTextContent("Date not recorded");
    expect(choices[2]).toHaveTextContent("Date not recorded");
  });
  expect(copies).toHaveBeenCalledWith({ signature: dated.signature, afterId: 0n, limit: 20 });
  expect(copies.mock.calls.filter(([request]) => request.limit === 100)).toHaveLength(0);
  expect(within(list).getAllByRole("button")[1]).toHaveAccessibleName(/Archive date not recorded/);
  await userEvent.click(screen.getByRole("button", { name: "Add to Restore list" }));
  expect(loadSelectionEntries("restore")[0].version?.id).toBe(dated.id);
});
it("updates the automatic Restore choice from later version pages and keeps a manual choice", async () => {
  const later = FileVersion.create({ ...saved, id: 3n, signature: new Uint8Array([3]), lastArchivedAtNs: 1750000000000000000n });
  const newest = FileVersion.create({ ...saved, id: 4n, signature: new Uint8Array([4]), lastArchivedAtNs: 1800000000000000000n });
  versions.mockImplementation(({ afterId }) =>
    call(
      afterId === 0n
        ? { versions: [saved], hasMore: true }
        : afterId === saved.id
          ? { versions: [later], hasMore: true }
          : { versions: [newest], hasMore: false },
    ),
  );
  render(<FileContent detail={detail()} onRefresh={refresh} />, { wrapper: MemoryRouter });

  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  await userEvent.click(screen.getByRole("button", { name: "Load more saved versions" }));
  const list = screen.getByLabelText("Saved content versions");
  await waitFor(() => {
    expect(within(list).getAllByRole("button")).toHaveLength(2);
    expect(within(list).getAllByRole("button")[0]).toHaveAttribute("aria-pressed", "true");
  });
  expect(within(list).getAllByRole("button")[0]).toHaveTextContent(contentTime(later.lastArchivedAtNs));
  await userEvent.click(within(list).getAllByRole("button")[1]);
  await userEvent.click(screen.getByRole("button", { name: "Load more saved versions" }));
  await waitFor(() => expect(within(list).getAllByRole("button")).toHaveLength(3));
  expect(within(list).getAllByRole("button")[0]).toHaveTextContent(contentTime(newest.lastArchivedAtNs));
  expect(within(list).getAllByRole("button")[2]).toHaveAttribute("aria-pressed", "true");
  expect(copies.mock.calls.filter(([request]) => request.limit === 100)).toHaveLength(0);
});
it("previews the newest saved version, labelled, when the original is unavailable", async () => {
  const value = detail();
  value.contentReference = undefined;
  value.contentSignature = new Uint8Array();
  value.entry!.status!.original = OriginalAvailability.UNAVAILABLE;
  preview.mockReturnValue(call({ availability: PreviewAvailability.READY, assets: [{ role: "thumbnail", url: "/saved-image" }] }));
  const { container } = render(<FileContent detail={value} onRefresh={refresh} />, { wrapper: MemoryRouter });
  expect(screen.getByTitle(/Local file unavailable/)).toBeInTheDocument();
  await waitFor(() => expect(container.querySelector("img")).toHaveAttribute("src", "/saved-image"));
  expect(versions).toHaveBeenCalledWith({ fileId: 7n, afterId: 0n, limit: 100 }, expect.objectContaining({ abort: expect.any(AbortSignal) }));
  expect(preview).toHaveBeenCalledWith({ signature: saved.signature }, expect.objectContaining({ abort: expect.any(AbortSignal) }));
  expect(screen.getByText("Preview of the latest saved version")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  expect(await screen.findByRole("region", { name: "Archived copies" })).toBeInTheDocument();
});
it("finds the newest archive time within one millisecond across version pages for an archive-only Preview", async () => {
  const value = detail();
  value.contentReference = undefined;
  value.contentSignature = new Uint8Array();
  const older = FileVersion.create({ ...saved, id: 3n, firstArchivedAtNs: 1700000000000000001n });
  const newer = FileVersion.create({ ...saved, id: 1n, signature: new Uint8Array([3]), lastArchivedAtNs: 1700000000000000002n });
  versions.mockImplementation(({ afterId }) => call(afterId ? { versions: [newer], hasMore: false } : { versions: [older], hasMore: true }));
  render(<FileContent detail={value} onRefresh={refresh} />, { wrapper: MemoryRouter });
  await waitFor(() => expect(preview).toHaveBeenCalledWith({ signature: newer.signature }, expect.objectContaining({ abort: expect.any(AbortSignal) })));
  expect(preview).not.toHaveBeenCalledWith({ signature: older.signature }, expect.objectContaining({ abort: expect.any(AbortSignal) }));
  expect(versions).toHaveBeenCalledWith({ fileId: 7n, afterId: 3n, limit: 100 }, expect.objectContaining({ abort: expect.any(AbortSignal) }));
});
it("shows no Preview when neither the original nor a saved version exists", async () => {
  versions.mockReturnValue(call({ versions: [], hasMore: false }));
  const value = detail();
  value.original = undefined;
  value.contentReference = undefined;
  value.contentSignature = new Uint8Array();
  const { container } = render(<FileContent detail={value} onRefresh={refresh} />, { wrapper: MemoryRouter });
  await waitFor(() =>
    expect(versions).toHaveBeenCalledWith({ fileId: 7n, afterId: 0n, limit: 100 }, expect.objectContaining({ abort: expect.any(AbortSignal) })),
  );
  expect(preview).not.toHaveBeenCalled();
  await waitFor(() => expect(container.querySelector(".file-detail-preview")).not.toBeInTheDocument());
  expect(screen.getByText("Location")).toBeInTheDocument();
  expect(screen.getByText("Saved versions only")).toBeInTheDocument();
});
it("replaces a current Preview with the labelled saved version once its guarded reference is gone", async () => {
  preview.mockImplementation(({ signature }) =>
    call({
      availability: PreviewAvailability.READY,
      assets: [{ role: "thumbnail", url: signature === saved.signature ? "/saved-image" : "/current-image" }],
    }),
  );
  const { container, rerender } = render(<FileContent detail={detail()} onRefresh={refresh} />, { wrapper: MemoryRouter });
  await waitFor(() => expect(container.querySelector("img")).toHaveAttribute("src", "/current-image"));
  const value = detail();
  value.contentReference = undefined;
  value.contentSignature = new Uint8Array();
  rerender(<FileContent detail={value} onRefresh={refresh} />);
  await waitFor(() => expect(container.querySelector("img")).toHaveAttribute("src", "/saved-image"));
  expect(screen.getByText("Preview of the latest saved version")).toBeInTheDocument();
});
it("isolates late Preview responses when changing saved versions", async () => {
  // Two versions of different content carry different signatures; the older one answers first.
  const older = FileVersion.create({ ...saved, id: 1n, signature: new Uint8Array([2]) });
  versions.mockReturnValue(call({ versions: [saved, older], hasMore: false }));
  let resolve!: (value: unknown) => void;
  preview.mockImplementation(({ signature }) =>
    signature === saved.signature
      ? {
          response: new Promise((done) => {
            resolve = done;
          }),
        }
      : call({ availability: PreviewAvailability.READY, assets: [{ role: "thumbnail", url: "/older" }] }),
  );
  const { container } = render(<FileContent detail={detail()} onRefresh={refresh} />, { wrapper: MemoryRouter });
  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  await screen.findByRole("region", { name: "Archived copies" });
  await userEvent.click(screen.getAllByRole("button", { name: /Archive date not recorded/ })[1]);
  await waitFor(() => expect(container.querySelector("img")).toHaveAttribute("src", "/older"));
  await act(async () => resolve({ availability: PreviewAvailability.READY, assets: [{ role: "thumbnail", url: "/stale" }] }));
  expect(container.querySelector("img")).toHaveAttribute("src", "/older");
});
it("retains copy pagination when refreshing a selected signature", async () => {
  const positions = Array.from({ length: 21 }, (_, index) => Position.create({ id: BigInt(index + 1) }));
  copies.mockImplementation(({ afterId }) => call({ positions: afterId ? positions.slice(20) : positions.slice(0, 20), hasMore: !afterId }));
  const { rerender } = render(<ContentCopies signature={new Uint8Array([1])} fileID={7n} />);
  await userEvent.click(await screen.findByRole("button", { name: "Load more copies" }));
  await waitFor(() => expect(screen.getAllByTestId("ExpandMoreRoundedIcon")).toHaveLength(21));
  rerender(<ContentCopies signature={new Uint8Array([1])} fileID={7n} />);
  await waitFor(() => expect(copies).toHaveBeenCalledTimes(4));
  expect(screen.getAllByTestId("ExpandMoreRoundedIcon")).toHaveLength(21);
});
it("shows copy availability and Restore guidance without a file-byte Open link", async () => {
  copies.mockReturnValue(call({ positions: [Position.create({ id: 1n, mediaId: 3n, health: PositionHealth.HEALTHY })], hasMore: false }));
  media.mockReturnValue(
    call({ media: [Media.create({ id: 3n, kind: MediaKind.VOLUME, mounted: true, capabilities: { read: MediaAccess.CONCURRENT_RANDOM } })] }),
  );
  render(<ContentCopies signature={new Uint8Array([1])} versionID={2n} />);
  await userEvent.click(await screen.findByRole("button", { name: /Volume · Available · Last check passed/ }));
  expect(await screen.findByText("Available for Restore")).toBeInTheDocument();
  expect(screen.queryByRole("link", { name: "Open copy" })).not.toBeInTheDocument();
  expect(screen.getByTestId("ExpandMoreRoundedIcon")).toBeInTheDocument();
});
it("does not fabricate a saved history for an unassociated entry", async () => {
  const value = detail();
  value.entry!.associatedFileId = undefined;
  value.entry!.reference = live;
  render(<FileContent detail={value} onRefresh={refresh} />, { wrapper: MemoryRouter });
  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  expect(screen.getByText("No saved versions are available to add to the Restore list.")).toBeInTheDocument();
  expect(versions).not.toHaveBeenCalled();
});
it("clears old copy rows immediately after changing signature", async () => {
  copies.mockReturnValueOnce(call({ positions: [Position.create({ id: 1n, path: "old-content" })], hasMore: false }));
  const { rerender } = render(<ContentCopies signature={new Uint8Array([1])} />);
  await screen.findByRole("button", { name: /Archive storage/ });
  await userEvent.click(screen.getByRole("button", { name: /Archive storage/ }));
  await screen.findByText("old-content");
  copies.mockReturnValueOnce({ response: new Promise(() => {}) });
  rerender(<ContentCopies signature={new Uint8Array([2])} />);
  expect(screen.queryByText("old-content")).not.toBeInTheDocument();
  expect(screen.getByRole("status")).toHaveTextContent("Loading archived copies");
});

it("removes only the selected saved version and never repeats a completed removal after refresh fails", async () => {
  versions.mockReturnValue(call({ versions: [saved, FileVersion.create({ ...saved, id: 3n })], hasMore: false }));
  removeVersion.mockReturnValue(call({}));
  const reload = vi.fn().mockRejectedValue(new Error("Catalog offline"));
  render(<FileContent detail={detail()} onRefresh={reload} />, { wrapper: MemoryRouter });
  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  await userEvent.click(await screen.findByRole("button", { name: "Remove version" }));
  const buttons = screen.getAllByRole("button", { name: "Remove version" });
  await userEvent.click(buttons.at(-1)!);
  await waitFor(() => expect(removeVersion).toHaveBeenCalledWith({ fileId: 7n, versionId: 3n, dryrun: false }));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(removeVersion).toHaveBeenCalledTimes(1);
  expect(report).toHaveBeenCalledWith(expect.stringContaining("Version removed, but refresh failed"));
  expect(screen.getAllByRole("button", { name: /Archive date not recorded/ })).toHaveLength(1);
});

it("retains the complete history and manual selection when a later refresh page fails", async () => {
  const later = FileVersion.create({ ...saved, id: 3n, sizeBytes: 33n });
  versions.mockImplementation(({ afterId }) => call({ versions: afterId ? [later] : [saved], hasMore: !afterId }));
  const page = render(<FileContent detail={detail()} onRefresh={refresh} />, { wrapper: MemoryRouter });
  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  await userEvent.click(await screen.findByRole("button", { name: "Load more saved versions" }));
  const list = screen.getByLabelText("Saved content versions");
  await waitFor(() => expect(within(list).getAllByRole("button")).toHaveLength(2));
  await userEvent.click(within(list).getAllByRole("button")[1]);
  let fail!: (error: Error) => void;
  const pending = {
    response: new Promise((_, reject) => {
      fail = reject;
    }),
  };
  const updated = FileVersion.create({ ...saved, sizeBytes: 999n });
  versions.mockImplementation(({ afterId }) => (afterId ? pending : call({ versions: [updated], hasMore: true })));
  page.rerender(<FileContent detail={detail()} onRefresh={refresh} />);
  await waitFor(() => expect(versions).toHaveBeenCalledTimes(4));
  expect(within(list).getAllByRole("button")[1]).toHaveTextContent("12 B");
  expect(within(list).getAllByRole("button")[1]).toHaveAttribute("aria-pressed", "true");
  await act(async () => fail(new Error("Second page offline")));
  await screen.findByText("Second page offline");
  expect(within(list).getAllByRole("button")).toHaveLength(2);
  expect(within(list).queryByText("999 B")).not.toBeInTheDocument();
  versions.mockImplementation(({ afterId }) => call({ versions: afterId ? [later] : [updated], hasMore: !afterId }));
  await userEvent.click(screen.getByRole("button", { name: "Retry" }));
  await within(list).findByText("999 B");
  expect(within(list).getAllByRole("button")[1]).toHaveAttribute("aria-pressed", "true");
  expect(versions.mock.calls.map(([request]) => request.limit)).toEqual([20, 20, 20, 20, 20, 20]);
});

it("cancels an obsolete history refresh before reading its next page", async () => {
  const later = FileVersion.create({ ...saved, id: 3n });
  versions.mockImplementation(({ afterId }) => call({ versions: afterId ? [later] : [saved], hasMore: !afterId }));
  const page = render(<FileContent detail={detail()} onRefresh={refresh} />, { wrapper: MemoryRouter });
  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  await userEvent.click(await screen.findByRole("button", { name: "Load more saved versions" }));
  await waitFor(() => expect(screen.getAllByRole("button", { name: /Archive date not recorded/ })).toHaveLength(2));
  let complete!: (reply: unknown) => void;
  versions.mockReturnValueOnce({
    response: new Promise((resolve) => {
      complete = resolve;
    }),
  });
  page.rerender(<FileContent detail={detail()} onRefresh={refresh} />);
  const obsoleteSignal = versions.mock.calls[2][1].abort as AbortSignal;
  versions.mockReturnValue(call({ versions: [later], hasMore: false }));
  page.rerender(<FileContent detail={detail()} onRefresh={refresh} />);
  await waitFor(() => expect(screen.getAllByRole("button", { name: /Archive date not recorded/ })).toHaveLength(1));
  expect(obsoleteSignal.aborted).toBe(true);
  await act(async () => complete({ versions: [saved], hasMore: true }));
  expect(versions).toHaveBeenCalledTimes(4);
  expect(screen.getAllByRole("button", { name: /Archive date not recorded/ })).toHaveLength(1);
});

it("stops an archive-only Preview lookup at a cancelled page boundary", async () => {
  let complete!: (reply: unknown) => void;
  versions.mockReturnValueOnce({
    response: new Promise((resolve) => {
      complete = resolve;
    }),
  });
  const original = detail();
  original.contentReference = undefined;
  original.contentSignature = new Uint8Array();
  const page = render(<FileContent detail={original} onRefresh={refresh} />, { wrapper: MemoryRouter });
  const signal = versions.mock.calls[0][1].abort as AbortSignal;
  page.rerender(<FileContent detail={detail()} onRefresh={refresh} />);
  expect(signal.aborted).toBe(true);
  await act(async () => complete({ versions: [saved], hasMore: true }));
  expect(versions).toHaveBeenCalledOnce();
  expect(preview.mock.calls.map(([request]) => request.signature)).toEqual([currentSignature]);
});

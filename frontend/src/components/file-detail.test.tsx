import { act, renderHook, screen, waitFor } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { EntryKind, FileOperationKind, FileOperationRef, FilesDetail, FileVersion, OriginalAvailability, PreviewAvailability } from "@/entity";
import { DetailModal, FileInspector, useFileDetail } from "./file-detail";
import { loadSelectionEntries } from "@/components/selection-waitlist-state";
const { get, versions, copies, preview } = vi.hoisted(() => ({ get: vi.fn(), versions: vi.fn(), copies: vi.fn(), preview: vi.fn() }));
vi.mock("@/api", async (original) => ({
  ...(await original<typeof import("@/api")>()),
  filesCli: {
    get: (...args: unknown[]) => ({ response: get(...args).response.then((detail: unknown) => ({ detail })) }),
    listVersions: versions,
    listCopies: copies,
  },
  previewCli: { get: preview },
  mediaCli: { list: () => ({ response: Promise.resolve({ media: [] }) }) },
}));
vi.mock("@/components/file-metadata-dialog", () => ({ FileMetadataDialog: () => null }));
const reply = <T,>(value: T) => ({ response: Promise.resolve(value) });
const ref = (id: bigint) => FileOperationRef.create({ target: { oneofKind: "fileId", fileId: id } });
const live = FileOperationRef.create({ target: { oneofKind: "location", location: { locationId: 4n, path: "folder/image.jpg" } } });
const saved = FileVersion.create({ id: 12n, fileId: 1n, signature: new Uint8Array([9]), sizeBytes: 55n, firstArchivedAtNs: 1788825600000000000n });
const detail = (id = 1n) =>
  FilesDetail.create({
    entry: {
      reference: ref(id),
      associatedFileId: id,
      name: `file-${id}.jpg`,
      kind: EntryKind.FILE,
      sizeBytes: 55n,
      status: { original: OriginalAvailability.PRESENT },
      operations: [FileOperationKind.ARCHIVE],
    },
    organization: { tags: ["keep"], note: "Organization stays" },
    original: { reference: live, sourceName: "Documents", path: "folder/image.jpg" },
    contentReference: live,
  });
const defer = <T,>() => {
  let resolve!: (value: T) => void;
  const response = new Promise<T>((done) => {
    resolve = done;
  });
  return { response, resolve };
};
beforeEach(() => {
  vi.clearAllMocks();
  sessionStorage.clear();
  get.mockImplementation(({ reference }) => reply(detail(reference.target.oneofKind === "fileId" ? reference.target.fileId : 1n)));
  versions.mockReturnValue(reply({ versions: [saved], hasMore: false }));
  copies.mockReturnValue(reply({ positions: [], hasMore: false }));
  preview.mockReturnValue(reply({ availability: PreviewAvailability.NOT_GENERATED, assets: [] }));
});
const refresh = async () => {};
const wrapper = MemoryRouter;
describe("controlled Files detail", () => {
  it("loads one detail and keeps version history lazy across ordinary renders", async () => {
    const { rerender } = render(<FileInspector target={live} name="image.jpg" onRefresh={refresh} />, { wrapper });
    await screen.findByText("Organization stays");
    expect(get).toHaveBeenCalledOnce();
    expect(versions).not.toHaveBeenCalled();
    rerender(<FileInspector target={FileOperationRef.clone(live)} name="image.jpg" onRefresh={refresh} />);
    expect(get).toHaveBeenCalledOnce();
    expect(screen.getByRole("link", { name: "Documents" })).toHaveAttribute("href", "/file?location=4");
    expect(screen.getByRole("link", { name: "folder/image.jpg" })).toHaveAttribute("href", "/file?location=4&path=folder&reveal=folder%2Fimage.jpg");
  });
  it("retains a coherent snapshot and selected tab during explicit refresh", async () => {
    const { rerender } = render(<FileInspector target={ref(1n)} onRefresh={refresh} />, { wrapper });
    await userEvent.click(await screen.findByRole("tab", { name: "Saved versions" }));
    await screen.findByRole("region", { name: "Archived copies" });
    const pending = defer<FilesDetail>();
    get.mockReturnValueOnce(pending);
    rerender(<FileInspector target={ref(1n)} refreshKey={1} onRefresh={refresh} />);
    expect(screen.getByRole("progressbar", { name: "Refreshing details" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Archived copies" })).toBeInTheDocument();
    await act(async () => pending.resolve(detail()));
    await waitFor(() => expect(screen.queryByRole("progressbar")).not.toBeInTheDocument());
    expect(screen.getByRole("tab", { name: "Saved versions" })).toHaveAttribute("aria-selected", "true");
  });
  it("keeps the selected tab when another File is selected", async () => {
    const { rerender } = render(<FileInspector target={ref(1n)} onRefresh={refresh} />, { wrapper });
    await userEvent.click(await screen.findByRole("tab", { name: "Saved versions" }));
    await screen.findByRole("region", { name: "Archived copies" });

    rerender(<FileInspector target={ref(2n)} onRefresh={refresh} />);
    await screen.findByRole("heading", { name: "file-2.jpg" });
    expect(screen.getByRole("tab", { name: "Saved versions" })).toHaveAttribute("aria-selected", "true");
    await waitFor(() =>
      expect(versions).toHaveBeenCalledWith({ fileId: 2n, afterId: 0n, limit: 20 }, expect.objectContaining({ abort: expect.any(AbortSignal) })),
    );
  });
  it("hides another entry's snapshot immediately and rejects its late response", async () => {
    const { result, rerender } = renderHook(({ target, revision }) => useFileDetail(target, revision), { initialProps: { target: ref(1n), revision: 0 } });
    await waitFor(() => expect(result.current.detail?.entry?.associatedFileId).toBe(1n));
    const a = defer<FilesDetail>();
    get.mockReturnValueOnce(a);
    rerender({ target: ref(1n), revision: 1 });
    const b = defer<FilesDetail>();
    get.mockReturnValueOnce(b);
    rerender({ target: ref(2n), revision: 1 });
    expect(result.current.detail).toBeUndefined();
    await act(async () => b.resolve(detail(2n)));
    await act(async () => a.resolve(detail(1n)));
    expect(result.current.detail?.entry?.associatedFileId).toBe(2n);
  });
  it("renders read failures and retries in the same panel", async () => {
    get.mockReturnValueOnce({ response: Promise.reject(new Error("Location unavailable")) });
    render(<FileInspector target={live} onRefresh={refresh} />, { wrapper });
    await screen.findByText("Location unavailable");
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByText("Organization stays");
  });
  it("does not request history or Preview for a directory", async () => {
    get.mockReturnValueOnce(reply(FilesDetail.create({ entry: { reference: live, name: "folder", kind: EntryKind.DIRECTORY } })));
    render(<FileInspector target={live} onRefresh={refresh} />, { wrapper });
    await screen.findByText("Folder");
    expect(versions).not.toHaveBeenCalled();
    expect(preview).not.toHaveBeenCalled();
  });
  it("dismisses the shared modal without publishing its pending reply", async () => {
    const pending = defer<FilesDetail>();
    get.mockReturnValueOnce(pending);
    const { rerender } = render(<DetailModal target={live} onRefresh={refresh} />, { wrapper });
    expect(screen.getByRole("status", { name: "Loading file details" })).toBeInTheDocument();
    rerender(<DetailModal onRefresh={refresh} />);
    await act(async () => pending.resolve(detail()));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
  it("uses the same detail surface in the Inspector and modal", async () => {
    const { container } = render(
      <>
        <FileInspector target={ref(1n)} onRefresh={refresh} />
        <DetailModal target={ref(1n)} onRefresh={refresh} onClose={() => {}} />
      </>,
      { wrapper },
    );
    await screen.findAllByRole("heading", { name: "file-1.jpg" });
    expect(container.querySelector(".detail-surface")).toBeInTheDocument();
    expect(document.querySelector("[role='dialog'] .detail-surface")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Close details" })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Add to Archive list", hidden: true })).toHaveLength(2);
  });
  it.each(["Inspector", "modal"])("adds to the same Archive waitlist from the %s and stays in the detail context", async (surface) => {
    render(
      surface === "Inspector" ? (
        <FileInspector target={ref(1n)} onRefresh={refresh} />
      ) : (
        <DetailModal target={ref(1n)} onRefresh={refresh} onClose={() => {}} />
      ),
      { wrapper },
    );
    await userEvent.click(await screen.findByRole("button", { name: "Add to Archive list" }));
    expect(screen.getByRole("heading", { name: "file-1.jpg" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "View list" })).toBeInTheDocument();
    expect(loadSelectionEntries("archive")).toHaveLength(1);
  });
  it("keeps the panel's shape while the next file loads and replaces it in place", async () => {
    const { rerender, container } = render(<FileInspector target={ref(1n)} onRefresh={refresh} />, { wrapper });
    await screen.findByText("Organization stays");
    const pending = defer<FilesDetail>();
    get.mockReturnValueOnce(pending);
    rerender(<FileInspector target={ref(2n)} onRefresh={refresh} />);
    // Nothing of the previous file stays readable, and the loading state occupies the layout the
    // loaded detail will take instead of collapsing the panel to a spinner.
    expect(screen.queryByText("Organization stays")).not.toBeInTheDocument();
    expect(screen.getByRole("status", { name: "Loading file details" })).toBeInTheDocument();
    expect(container.querySelector(".file-detail-skeleton-frame")).toBeInTheDocument();
    await act(async () => pending.resolve(detail(2n)));
    expect(await screen.findByText("Organization stays")).toBeInTheDocument();
    expect(screen.queryByRole("status", { name: "Loading file details" })).not.toBeInTheDocument();
  });
  it("preserves the loaded version window on explicit detail refresh", async () => {
    versions.mockImplementation(({ afterId }) => reply({ versions: [afterId ? FileVersion.create({ ...saved, id: 10n }) : saved], hasMore: !afterId }));
    const { rerender } = render(<FileInspector target={ref(1n)} onRefresh={refresh} />, { wrapper });
    await userEvent.click(await screen.findByRole("tab", { name: "Saved versions" }));
    await userEvent.click(await screen.findByRole("button", { name: "Load more saved versions" }));
    await waitFor(() => expect(screen.getByLabelText("Saved content versions").children).toHaveLength(2));
    rerender(<FileInspector target={ref(1n)} refreshKey={1} onRefresh={refresh} />);
    await waitFor(() => expect(versions).toHaveBeenCalledTimes(4));
    expect(screen.getByLabelText("Saved content versions").children).toHaveLength(2);
  });
});

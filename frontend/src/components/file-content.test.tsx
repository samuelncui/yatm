import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { beforeEach, expect, it, vi } from "vitest";
import { File, FileScope, FileStateReply, FileVersion, Location, LocationEntry, OnlineBinding, ContentCoverage, PreviewManifest, Position } from "@/entity";
import { ContentCopies, FileContent } from "./file-content";

const { getState, listVersions, getVersion, listCopies, locationEntry } = vi.hoisted(() => ({
  getState: vi.fn(),
  listVersions: vi.fn(),
  getVersion: vi.fn(),
  listCopies: vi.fn(),
  locationEntry: vi.fn(),
}));
vi.mock("@/api", () => ({
  fileBase: "/files",
  cli: { mediaList: () => ({ response: Promise.resolve({ media: [] }) }) },
  fileCatalogCli: { getState, listVersions, getVersion, listCopies },
  locationCli: { getEntry: locationEntry },
}));
vi.mock("@/components/file-preview", () => ({ PreviewMedia: () => <div data-testid="current-preview" /> }));
const source = Location.create({ id: 4n, name: "Pictures", rootPath: "/pictures", binding: OnlineBinding.CONFIRMED, revision: 9n, bindingToken: "binding-a" });
const observedEntry = (state: FileStateReply) => {
  const original = state.original!;
  return LocationEntry.create({
    path: original.path,
    file: { id: original.fileId },
    original,
    reference: {
      locationId: original.locationId,
      path: original.path,
      bindingToken: state.location?.bindingToken,
      facts: { size: original.size, mode: original.mode, mtimeNs: original.mtimeNs },
    },
  });
};
beforeEach(() => {
  vi.clearAllMocks();
  getState.mockReturnValue({
    response: Promise.resolve(
      FileStateReply.create({ original: { fileId: 7n, locationId: 4n, path: "image.jpg", observedBindingToken: "binding-a" }, location: source }),
    ),
  });
  listVersions.mockReturnValue({ response: Promise.resolve({ versions: [], hasMore: false }) });
  getVersion.mockReturnValue({ response: Promise.resolve({}) });
  listCopies.mockReturnValue({ response: Promise.resolve({ positions: [], hasMore: false }) });
  locationEntry.mockReset().mockImplementation(() => ({ response: getState.mock.results.at(-1)!.value.response.then(observedEntry) }));
});

it("refreshes an unchanged successful File snapshot without reloading on ordinary renders", async () => {
  const file = File.create({ id: 7n, name: "image.jpg" });
  const { rerender } = render(
    <MemoryRouter>
      <FileContent file={file} />
    </MemoryRouter>,
  );
  await screen.findByText("Available");
  rerender(
    <MemoryRouter>
      <FileContent file={file} organization={<p>Organization updated</p>} />
    </MemoryRouter>,
  );
  expect(getState).toHaveBeenCalledTimes(1);
  rerender(
    <MemoryRouter>
      <FileContent file={File.create(file)} />
    </MemoryRouter>,
  );
  await waitFor(() => expect(getState).toHaveBeenCalledTimes(2));
  await screen.findByText("Available");
  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  expect(getState).toHaveBeenCalledTimes(2);
});

it("retains the selected version and loaded version pages across a File refresh", async () => {
  const file = File.create({ id: 7n, name: "image.jpg" });
  const versions = Array.from({ length: 21 }, (_, index) =>
    FileVersion.create({ id: BigInt(21 - index), fileId: 7n, firstArchivedAtMs: BigInt(21000 - index * 1000) }),
  );
  getState.mockReturnValue({ response: Promise.resolve(FileStateReply.create({ latestVersion: versions[0] })) });
  listVersions.mockImplementation(({ afterId }) => ({
    response: Promise.resolve({
      versions: (afterId ? versions.slice(20) : versions.slice(0, 20)).map((value) => FileVersion.create(value)),
      hasMore: !afterId,
    }),
  }));
  const { rerender } = render(
    <MemoryRouter>
      <FileContent file={file} />
    </MemoryRouter>,
  );
  await userEvent.click(await screen.findByRole("tab", { name: "Saved versions" }));
  await userEvent.click(screen.getByRole("button", { name: "Load more saved versions" }));
  const list = screen.getByLabelText("Saved content versions");
  await waitFor(() => expect(within(list).getAllByRole("button")).toHaveLength(21));
  await userEvent.click(within(list).getAllByRole("button").at(-1)!);
  expect(screen.getByRole("link", { name: "Restore this version" })).toHaveAttribute("href", "/restore?version_id=1");
  getVersion.mockReturnValue({ response: Promise.resolve({ preview: PreviewManifest.create({ fileSignature: new Uint8Array([1]) }) }) });
  rerender(
    <MemoryRouter>
      <FileContent file={File.create(file)} />
    </MemoryRouter>,
  );
  await waitFor(() => expect(listVersions).toHaveBeenCalledTimes(4));
  await waitFor(() => expect(screen.queryByLabelText("Refreshing content information")).not.toBeInTheDocument());
  expect(screen.getByRole("tab", { name: "Saved versions" })).toHaveAttribute("aria-selected", "true");
  expect(within(list).getAllByRole("button")).toHaveLength(21);
  expect(within(list).getAllByRole("button").at(-1)).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("link", { name: "Restore this version" })).toHaveAttribute("href", "/restore?version_id=1");
  expect(getVersion).toHaveBeenCalledTimes(3);
  expect(await screen.findByTestId("current-preview")).toBeInTheDocument();
});

it("preserves loaded archived-copy pages when the current original is refreshed", async () => {
  const positions = Array.from({ length: 21 }, (_, index) => Position.create({ id: BigInt(index + 1) }));
  listCopies.mockImplementation(({ afterId }) => ({
    response: Promise.resolve({ positions: afterId ? positions.slice(20) : positions.slice(0, 20), hasMore: !afterId }),
  }));
  const { rerender } = render(<ContentCopies signature={new Uint8Array([1])} fileID={7n} />);
  await userEvent.click(await screen.findByRole("button", { name: "Load more copies" }));
  await waitFor(() => expect(screen.getAllByRole("article")).toHaveLength(21));
  rerender(<ContentCopies signature={new Uint8Array([1])} fileID={7n} />);
  await waitFor(() => expect(screen.queryByRole("progressbar")).not.toBeInTheDocument());
  expect(screen.getAllByRole("article")).toHaveLength(21);
});

it("rejects stale version-preview errors and clears a transient error after the next successful observation", async () => {
  const file = File.create({ id: 7n, name: "image.jpg" });
  const version = FileVersion.create({ id: 1n, fileId: 7n });
  getState.mockImplementation(() => ({ response: Promise.resolve(FileStateReply.create({ latestVersion: version })) }));
  listVersions.mockImplementation(() => ({ response: Promise.resolve({ versions: [FileVersion.create(version)], hasMore: false }) }));
  let rejectOld!: (error: Error) => void;
  getVersion.mockReturnValueOnce({
    response: new Promise((_, reject) => {
      rejectOld = reject;
    }),
  });
  getVersion.mockImplementation(() => ({ response: Promise.resolve({ preview: PreviewManifest.create({ fileSignature: new Uint8Array([1]) }) }) }));
  const { rerender } = render(
    <MemoryRouter>
      <FileContent file={file} />
    </MemoryRouter>,
  );
  await userEvent.click(await screen.findByRole("tab", { name: "Saved versions" }));
  rerender(
    <MemoryRouter>
      <FileContent file={File.create(file)} />
    </MemoryRouter>,
  );
  await screen.findByTestId("current-preview");
  await act(async () => {
    rejectOld(new Error("Stale preview request"));
  });
  expect(screen.queryByText("Stale preview request")).not.toBeInTheDocument();
  getVersion.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Preview temporarily unavailable")) }));
  rerender(
    <MemoryRouter>
      <FileContent file={File.create(file)} />
    </MemoryRouter>,
  );
  await screen.findByText("Preview temporarily unavailable");
  expect(screen.getByTestId("current-preview")).toBeInTheDocument();
  rerender(
    <MemoryRouter>
      <FileContent file={File.create(file)} />
    </MemoryRouter>,
  );
  await waitFor(() => expect(screen.queryByText("Preview temporarily unavailable")).not.toBeInTheDocument());
  expect(screen.getByTestId("current-preview")).toBeInTheDocument();
});

it("rejects a superseded content reply and does not show a preview against different original facts", async () => {
  const file = File.create({ id: 7n, name: "image.jpg", hash: new Uint8Array([1]), size: 12n });
  const current = FileStateReply.create({
    original: {
      fileId: 7n,
      locationId: 4n,
      path: "image.jpg",
      observedBindingToken: "binding-a",
      signature: new Uint8Array([91]),
      sha256: file.hash,
      size: file.size,
    },
    location: source,
    coverage: ContentCoverage.ARCHIVED_CONTENT,
    archivedCopies: 1n,
  });
  getState.mockReturnValueOnce({ response: Promise.resolve(current) });
  let resolveOld!: (value: FileStateReply) => void;
  getState.mockReturnValueOnce({
    response: new Promise<FileStateReply>((resolve) => {
      resolveOld = resolve;
    }),
  });
  getState.mockReturnValueOnce({
    response: Promise.resolve(
      FileStateReply.create({
        original: {
          fileId: 7n,
          locationId: 4n,
          path: "image.jpg",
          observedBindingToken: "binding-a",
          signature: new Uint8Array([92]),
          sha256: new Uint8Array([2]),
          size: 12n,
        },
        location: source,
        coverage: ContentCoverage.NO_ARCHIVED_COPY,
      }),
    ),
  });
  const { rerender } = render(
    <MemoryRouter>
      <FileContent file={file} currentPreview={PreviewManifest.create({ fileSignature: new Uint8Array([1]) })} />
    </MemoryRouter>,
  );
  await screen.findByTestId("current-preview");
  rerender(
    <MemoryRouter>
      <FileContent file={File.create({ ...file, hash: new Uint8Array([2]) })} currentPreview={PreviewManifest.create({ fileSignature: new Uint8Array([2]) })} />
    </MemoryRouter>,
  );
  expect(screen.queryByTestId("current-preview")).not.toBeInTheDocument();
  rerender(
    <MemoryRouter>
      <FileContent file={File.create({ ...file, hash: new Uint8Array([2]) })} currentPreview={PreviewManifest.create({ fileSignature: new Uint8Array([2]) })} />
    </MemoryRouter>,
  );
  await screen.findByTestId("current-preview");
  await act(async () => {
    resolveOld(current);
  });
  expect(screen.queryByText("Local file present · Current content backed up")).not.toBeInTheDocument();
  expect(screen.getByTestId("current-preview")).toBeInTheDocument();
});
const Selected = () => <div data-testid="selection-scope">{useLocation().state.selections[0].selection.scope}</div>;

it("does not let an old live observation restore green coverage or a current preview after an external edit", async () => {
  const file = File.create({ id: 7n, name: "image.jpg", size: 12n, hash: new Uint8Array([1]) });
  const state = FileStateReply.create({
    location: source,
    original: {
      fileId: 7n,
      locationId: 4n,
      path: "image.jpg",
      observedBindingToken: "binding-a",
      size: 12n,
      mode: 420,
      mtimeNs: 100n,
      signature: new Uint8Array([91]),
      sha256: file.hash,
    },
    coverage: ContentCoverage.ARCHIVED_CONTENT,
    archivedCopies: 1n,
    latestVersion: { id: 1n, fileId: 7n },
  });
  getState.mockReturnValue({ response: Promise.resolve(state) });
  listVersions.mockReturnValue({ response: Promise.resolve({ versions: [state.latestVersion], hasMore: false }) });
  let resolveOld!: (entry: LocationEntry) => void;
  locationEntry.mockReturnValueOnce({
    response: new Promise<LocationEntry>((resolve) => {
      resolveOld = resolve;
    }),
  });
  const changed = observedEntry(state);
  changed.reference!.facts!.size = 13n;
  changed.original!.signature = new Uint8Array();
  locationEntry.mockReturnValue({ response: Promise.resolve(changed) });
  const renderFile = () => (
    <MemoryRouter>
      <FileContent file={File.create(file)} currentPreview={PreviewManifest.create({ fileSignature: new Uint8Array([91]) })} />
    </MemoryRouter>
  );
  const { rerender } = render(renderFile());
  await waitFor(() => expect(locationEntry).toHaveBeenCalledTimes(1));
  expect(screen.queryByText("Local file present · Current content backed up")).not.toBeInTheDocument();
  rerender(renderFile());
  await screen.findByText("Changed");
  await act(async () => resolveOld(observedEntry(state)));
  expect(screen.getByText("Local file not checked · Backup not checked")).toBeInTheDocument();
  expect(screen.queryByTestId("current-preview")).not.toBeInTheDocument();
  expect(screen.queryByRole("link", { name: "Open" })).not.toBeInTheDocument();
  expect(screen.getByRole("link", { name: "image.jpg" })).toHaveAttribute("href", "/file?location=4&reveal=image.jpg");
  expect(screen.getByRole("link", { name: "Back up file" })).not.toHaveAttribute("aria-disabled", "true");
  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  expect(within(screen.getByLabelText("Saved content versions")).getAllByRole("button")).toHaveLength(1);
});

it("retains saved versions when the exact original cannot be accessed", async () => {
  getState.mockReturnValue({
    response: Promise.resolve(
      FileStateReply.create({
        location: source,
        original: { fileId: 7n, locationId: 4n, path: "missing.jpg", observedBindingToken: "binding-a" },
        latestVersion: { id: 1n, fileId: 7n },
        coverage: ContentCoverage.ARCHIVED_CONTENT,
        archivedCopies: 1n,
        summary: { originalAvailability: 1, currentObservationValid: true, signatureKnown: true, restorableCurrentCopies: 1n },
      }),
    ),
  });
  locationEntry.mockImplementation(() => ({ response: Promise.reject(new Error("File no longer exists")) }));
  render(
    <MemoryRouter>
      <FileContent file={File.create({ id: 7n, name: "image.jpg" })} />
    </MemoryRouter>,
  );
  await screen.findByText("File no longer exists");
  expect(screen.getByText("Unavailable")).toBeInTheDocument();
  expect(screen.queryByRole("link", { name: "Open" })).not.toBeInTheDocument();
  expect(screen.queryByText("Local file present · Current content backed up")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
  expect(within(screen.getByLabelText("Saved content versions")).getAllByRole("button")).toHaveLength(1);
});

it("backs up an explicit Location original independently of the Library default visibility", async () => {
  render(
    <MemoryRouter>
      <Routes>
        <Route path="/" element={<FileContent file={File.create({ id: 7n, name: "image.jpg" })} />} />
        <Route path="/backup" element={<Selected />} />
      </Routes>
    </MemoryRouter>,
  );
  await waitFor(() => expect(screen.getByRole("link", { name: "Back up file" })).not.toHaveAttribute("aria-disabled", "true"));
  await userEvent.click(screen.getByRole("link", { name: "Back up file" }));
  expect(screen.getByTestId("selection-scope")).toHaveTextContent(String(FileScope.ALL));
});

it("allows a restored observation without full analysis but rejects a response from an old root binding", async () => {
  const location = Location.create({ ...source, binding: OnlineBinding.CONFIRMED });
  getState.mockReturnValue({
    response: Promise.resolve(
      FileStateReply.create({ location, original: { fileId: 7n, locationId: 4n, path: "image.jpg", observedBindingToken: "binding-a" } }),
    ),
  });
  const { rerender } = render(
    <MemoryRouter>
      <FileContent file={File.create({ id: 7n, name: "image.jpg" })} />
    </MemoryRouter>,
  );
  await screen.findByText("Available");
  expect(screen.getByRole("link", { name: "Back up file" })).not.toHaveAttribute("aria-disabled", "true");
  locationEntry.mockReturnValue({
    response: Promise.resolve(
      observedEntry(FileStateReply.create({ location, original: { fileId: 7n, locationId: 4n, path: "image.jpg", observedBindingToken: "binding-a" } })),
    ),
  });
  getState.mockReturnValue({
    response: Promise.resolve(
      FileStateReply.create({
        location: { ...location, bindingToken: "binding-b" },
        original: { fileId: 7n, locationId: 4n, path: "image.jpg", observedBindingToken: "binding-a" },
      }),
    ),
  });
  rerender(
    <MemoryRouter>
      <FileContent file={File.create({ id: 7n, name: "image.jpg" })} />
    </MemoryRouter>,
  );
  await waitFor(() => expect(screen.getByRole("link", { name: "Back up file" })).toHaveAttribute("aria-disabled", "true"));
});

it("reloads content observations after the same File is refreshed", async () => {
  const file = File.create({ id: 7n, name: "image.jpg" });
  const { rerender } = render(
    <MemoryRouter>
      <FileContent file={file} />
    </MemoryRouter>,
  );
  await screen.findByText("Local file not checked · Backup not checked");
  getState.mockReturnValue({
    response: Promise.resolve(
      FileStateReply.create({
        original: { fileId: 7n, locationId: 4n, path: "image.jpg", observedBindingToken: "binding-a" },
        location: source,
        coverage: ContentCoverage.ARCHIVED_CONTENT,
        archivedCopies: 1n,
        summary: { originalAvailability: 1, currentObservationValid: true, signatureKnown: true, restorableCurrentCopies: 1n },
      }),
    ),
  });
  rerender(
    <MemoryRouter>
      <FileContent file={File.create({ ...file, contentSummary: { hasOriginal: true, hasVersions: true, signatureKnown: true, archivedCopies: 1n } })} />
    </MemoryRouter>,
  );
  await screen.findByText("Local file present · Current content backed up");
});

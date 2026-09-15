import { fireEvent, render as testingRender, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import type { ReactNode } from "react";
const render = (node: ReactNode) => testingRender(node, { wrapper: MemoryRouter });
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { getState, listVersions, listCopies, getEntry, listMedia, getVersion } = vi.hoisted(() => ({
  getState: vi.fn(),
  listVersions: vi.fn(),
  listCopies: vi.fn(),
  getEntry: vi.fn(),
  listMedia: vi.fn(),
  getVersion: vi.fn(),
}));
vi.mock("@/api", async (original) => ({
  ...(await original<typeof import("@/api")>()),
  fileCatalogCli: { getState, listVersions, listCopies, getVersion },
  locationCli: { getEntry },
  cli: { mediaList: listMedia },
}));
vi.mock("@/components/file-metadata-dialog", () => ({
  FileMetadataDialog: ({ files, open }: { files: Array<{ name: string }>; open: boolean }) => (open ? <div>Edit metadata for {files[0]?.name}</div> : null),
}));

import {
  File,
  FileVersion,
  FileStateReply,
  Location,
  LocationEntry,
  OnlineBinding,
  Media,
  MediaAccess,
  MediaKind,
  Position,
  PreviewManifest,
  ContentCoverage,
} from "@/entity";
import { FileInspector } from "@/pages/file-detail";

afterEach(() => vi.unstubAllGlobals());
const refresh = async () => {};
const call = (value: unknown) => ({ response: Promise.resolve(value) });
const location = Location.create({ id: 1n, name: "Originals", revision: 2n, binding: OnlineBinding.CONFIRMED, bindingToken: "binding-a" });
beforeEach(() => {
  getState.mockImplementation(({ fileId }: { fileId: bigint }) =>
    call(
      FileStateReply.create({
        original: { fileId, locationId: 1n, path: "fixture.txt", signature: new Uint8Array([1]), observedBindingToken: "binding-a" },
        location,
        coverage: ContentCoverage.ARCHIVED_CONTENT,
        archivedCopies: 1n,
      }),
    ),
  );
  listVersions.mockReturnValue(call({ versions: [], hasMore: false }));
  listCopies.mockReturnValue(call({ positions: [], hasMore: false }));
  getEntry.mockImplementation(() => ({
    response: getState.mock.results.at(-1)!.value.response.then((state: FileStateReply) => {
      const original = state.original!;
      return LocationEntry.create({
        path: original.path,
        file: { id: original.fileId },
        original,
        reference: {
          locationId: original.locationId,
          path: original.path,
          bindingToken: state.location!.bindingToken,
          facts: { size: original.size, mtimeNs: original.mtimeNs, mode: original.mode },
        },
      });
    }),
  }));
  getVersion.mockReturnValue(call({}));
});

describe("FileInspector online copies", () => {
  it("resolves a live row's explicit Library association to its saved versions and shared physical copies", async () => {
    const saved = FileVersion.create({
      id: 19n,
      fileId: 4n,
      signature: new Uint8Array([9]),
      size: 53n,
      firstArchivedAtMs: 1788825600000n,
    });
    getState.mockReturnValue(
      call(
        FileStateReply.create({
          original: { fileId: 4n, signature: saved.signature },
          latestVersion: saved,
          summary: { originalAvailability: 1, signatureKnown: true, currentObservationValid: true, hasVersions: true, restorableVersionCopies: 1n },
          coverage: ContentCoverage.ARCHIVED_CONTENT,
          archivedCopies: 2n,
        }),
      ),
    );
    listVersions.mockReturnValue(call({ versions: [saved], hasMore: false }));
    listCopies.mockReturnValue(call({ positions: [Position.create({ id: 7n, mediaId: 5n }), Position.create({ id: 8n, mediaId: 6n })], hasMore: false }));
    listMedia.mockReturnValue(call({ media: [Media.create({ id: 5n, name: "Archive A" }), Media.create({ id: 6n, name: "Archive B" })] }));
    const file = File.create({ id: 4n, name: "handbook.md" });
    render(
      <FileInspector
        selected={{ id: "location-file:1:handbook.md", physicalLocationID: "1", libraryFileID: 4n, name: file.name }}
        loading={false}
        detail={{ file, children: [], nextCursor: "", scope: 1 }}
        onRefresh={refresh}
      />,
    );

    await userEvent.click(await screen.findByRole("tab", { name: "Saved versions" }));
    expect(within(screen.getByLabelText("Saved content versions")).getAllByRole("button")).toHaveLength(1);
    expect(screen.getByRole("link", { name: "Restore this version" })).toHaveAttribute("href", "/restore?version_id=19");
    expect(await screen.findByText("Archive A")).toBeInTheDocument();
    expect(screen.getByText("Archive B")).toBeInTheDocument();
    expect(screen.queryByText("No saved versions yet")).not.toBeInTheDocument();
    expect(screen.queryByText("Back up this file to save its first version.")).not.toBeInTheDocument();
  });

  it("shows the actual location without redundant headings or empty location explanations", async () => {
    getState.mockReturnValue(
      call(
        FileStateReply.create({
          original: { fileId: 4n, locationId: 1n, path: "camera/photo.png", signature: new Uint8Array([1]), observedBindingToken: "binding-a" },
          location: { id: 1n, name: "Photos", rootPath: "/Volumes/Photos", revision: 2n, binding: OnlineBinding.CONFIRMED, bindingToken: "binding-a" },
        }),
      ),
    );
    const file = File.create({ id: 4n, name: "cover.png" });
    render(
      <FileInspector selected={{ id: "4", name: file.name }} loading={false} detail={{ file, children: [], nextCursor: "", scope: 1 }} onRefresh={refresh} />,
    );
    expect(await screen.findByRole("link", { name: "Photos" })).toHaveAttribute("href", "/file?location=1");
    expect(screen.getByRole("link", { name: "camera/photo.png" })).toHaveAttribute("href", "/file?location=1&path=camera&reveal=camera%2Fphoto.png");
    expect(screen.queryByRole("heading", { name: "Original location" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Open" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Download" })).not.toBeInTheDocument();
  });

  it("keeps saved version dates, copies and restore identity separate from the current original", async () => {
    const saved = FileVersion.create({ id: 19n, fileId: 4n, signature: new Uint8Array([9]), size: 25n });
    getState.mockReturnValue(
      call(
        FileStateReply.create({
          location,
          original: { fileId: 4n, locationId: 1n, observedBindingToken: "binding-a", signature: new Uint8Array([1]) },
          latestVersion: saved,
          summary: { originalAvailability: 1, currentObservationValid: true, signatureKnown: true, hasVersions: true, restorableVersionCopies: 1n },
          coverage: ContentCoverage.NO_ARCHIVED_COPY,
        }),
      ),
    );
    listVersions.mockReturnValue(call({ versions: [saved], hasMore: false }));
    const file = File.create({ id: 4n, name: "notes.txt", note: "Keep my organization" });
    render(
      <FileInspector
        selected={{ id: "4", name: "notes.txt", isDir: false }}
        loading={false}
        detail={{ file, children: [], nextCursor: "", scope: 1 }}
        onRefresh={refresh}
      />,
    );
    expect(await screen.findByText(/Changes not backed up/)).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /Changes not backed up/ })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Saved versions" }));
    expect(screen.getByText("Archive date not recorded")).toBeInTheDocument();
    expect(screen.queryByText("The original backup date was not recorded in the archive metadata.")).not.toBeInTheDocument();
    expect(screen.queryByText(/Last backed up/)).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Restore this version" })).toHaveAttribute("href", "/restore?version_id=19");
    await waitFor(() => expect(listCopies).toHaveBeenLastCalledWith({ signature: new Uint8Array([9]), afterId: 0n, limit: 20 }));
    expect(screen.getByText(/This version cannot currently be restored/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("tab", { name: "Overview" }));
    expect(screen.getByText("Keep my organization")).toBeInTheDocument();
  });

  it("selects each version's own thumbnail, copies and restore target without changing File organization", async () => {
    const versions = [25n, 52n, 77n].map((size, index) =>
      FileVersion.create({
        id: BigInt(19 + index),
        fileId: 4n,
        signature: new Uint8Array([9 + index]),
        size,
        firstArchivedAtMs: 1788825600000n + BigInt(index) * 86400000n,
        lastArchivedAtMs: 1788825600000n + BigInt(index) * 86400000n,
      }),
    );
    getState.mockReturnValue(call(FileStateReply.create({ latestVersion: versions[2] })));
    listVersions.mockReturnValue(call({ versions, hasMore: false }));
    getVersion.mockImplementation(({ id }: { id: bigint }) =>
      call({
        preview: PreviewManifest.create({
          generator: "image",
          fileSignature: versions.find((version) => version.id === id)!.signature,
          assets: [{ name: "thumbnail.png", role: "thumbnail", mediaType: "image/png", width: 320, height: 180 }],
        }),
      }),
    );
    const file = File.create({ id: 4n, name: "archive-room.png", note: "Keep my organization" });
    const { container } = render(
      <FileInspector
        selected={{ id: "4", name: file.name, isDir: false }}
        loading={false}
        detail={{ file, children: [], nextCursor: "", scope: 1 }}
        onRefresh={refresh}
      />,
    );
    await userEvent.click(await screen.findByRole("tab", { name: "Saved versions" }));
    const choices = within(screen.getByLabelText("Saved content versions")).getAllByRole("button");
    expect(choices).toHaveLength(3);
    expect(screen.queryByText("Archive date not recorded")).not.toBeInTheDocument();
    for (const [index, version] of [...versions].reverse().entries()) {
      await userEvent.click(choices[index]);
      expect(choices[index]).toHaveAttribute("aria-pressed", "true");
      expect(screen.getByRole("link", { name: "Restore this version" })).toHaveAttribute("href", `/restore?version_id=${version.id}`);
      await waitFor(() => expect(listCopies).toHaveBeenLastCalledWith({ signature: version.signature, afterId: 0n, limit: 20 }));
      await waitFor(() =>
        expect(container.querySelector(".content-preview img")).toHaveAttribute(
          "src",
          `http://localhost:5173/files/previews/4/thumbnail?version_id=${version.id}`,
        ),
      );
    }
    await userEvent.click(screen.getByRole("tab", { name: "Overview" }));
    expect(screen.getByText("Keep my organization")).toBeInTheDocument();
    expect(container.querySelector(".content-preview img")).not.toBeInTheDocument();
    expect(screen.getByText("Local file not checked · Backup not checked")).toBeInTheDocument();
    expect(screen.queryByText("No original location linked.")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "View saved versions" })).not.toBeInTheDocument();
  });

  it("does not display a previous version's delayed thumbnail after selecting another version", async () => {
    const older = FileVersion.create({ id: 19n, fileId: 4n, signature: new Uint8Array([9]), size: 25n });
    const latest = FileVersion.create({ id: 20n, fileId: 4n, signature: new Uint8Array([10]), size: 30n });
    getState.mockReturnValue(call(FileStateReply.create({ latestVersion: latest })));
    listVersions.mockReturnValue(call({ versions: [older, latest], hasMore: false }));
    let completeOld!: (reply: { preview: PreviewManifest }) => void;
    const pending = new Promise<{ preview: PreviewManifest }>((resolve) => {
      completeOld = resolve;
    });
    getVersion.mockImplementation(({ id }: { id: bigint }) => (id === older.id ? { response: pending } : call({})));
    const file = File.create({ id: 4n, name: "image.png" });
    const { container } = render(
      <FileInspector selected={{ id: "4", name: file.name }} loading={false} detail={{ file, children: [], nextCursor: "", scope: 1 }} onRefresh={refresh} />,
    );
    await userEvent.click(await screen.findByRole("tab", { name: "Saved versions" }));
    const choices = within(screen.getByLabelText("Saved content versions")).getAllByRole("button");
    await userEvent.click(choices[1]);
    await userEvent.click(choices[0]);
    completeOld({
      preview: PreviewManifest.create({
        generator: "image",
        fileSignature: older.signature,
        assets: [{ name: "thumbnail.png", role: "thumbnail", mediaType: "image/png", width: 320, height: 180 }],
      }),
    });
    await waitFor(() => expect(screen.getByRole("link", { name: "Restore this version" })).toHaveAttribute("href", "/restore?version_id=20"));
    expect(container.querySelector(".content-preview img")).not.toBeInTheDocument();
  });

  it("offers Open and metadata editing without a download action for mounted concurrent-random Volumes", async () => {
    const file = File.create({ id: 4n, name: "fixture.txt", size: 7n, tags: ["archive", "favorite"], note: "Keep this copy" });
    const online = Media.create({
      id: 5n,
      kind: MediaKind.VOLUME,
      identity: "online",
      mounted: true,
      capabilities: { read: MediaAccess.CONCURRENT_RANDOM, write: MediaAccess.CONCURRENT_RANDOM },
    });
    const position = Position.create({ id: 7n, mediaId: online.id, path: "fixture.txt" });
    listCopies.mockReturnValue(call({ positions: [position], hasMore: false }));
    listMedia.mockReturnValue(call({ media: [online] }));

    render(
      <FileInspector
        selected={{ id: "4", name: "fixture.txt", isDir: false }}
        loading={false}
        detail={{ file, children: [], nextCursor: "", scope: 1 }}
        onRefresh={refresh}
      />,
    );

    expect(await screen.findByRole("link", { name: "Open copy" })).toHaveAttribute("href", "http://localhost:5173/files/content/7?");
    expect(screen.queryByRole("link", { name: "Download copy" })).not.toBeInTheDocument();
    expect(screen.getByText("archive")).toBeInTheDocument();
    expect(screen.getByText("favorite")).toBeInTheDocument();
    expect(screen.getByText("Keep this copy")).toBeInTheDocument();

    const editMetadata = screen.getByRole("button", { name: "Edit tags & note" });
    expect(editMetadata.querySelector('[data-testid="EditOutlinedIcon"]')).toBeInTheDocument();
    await userEvent.click(editMetadata);
    expect(screen.getByText("Edit metadata for fixture.txt")).toBeInTheDocument();
  });

  it("keeps matching File details visible while refreshing", () => {
    const file = File.create({ id: 4n, name: "fixture.txt", note: "Stable details" });
    render(
      <FileInspector
        selected={{ id: "4", name: "fixture.txt", isDir: false }}
        loading
        detail={{ file, children: [], nextCursor: "", scope: 1 }}
        onRefresh={refresh}
      />,
    );

    expect(screen.getByText("Stable details")).toBeInTheDocument();
    expect(screen.getAllByRole("progressbar")[0]).toBeInTheDocument();
  });

  it("does not expose direct content for an unmounted Volume", () => {
    const file = File.create({ id: 4n, name: "fixture.txt", size: 7n });
    const offline = Media.create({
      id: 5n,
      kind: MediaKind.VOLUME,
      identity: "offline",
      mounted: false,
      capabilities: { read: MediaAccess.CONCURRENT_RANDOM, write: MediaAccess.CONCURRENT_RANDOM },
    });
    const position = Position.create({ id: 7n, mediaId: offline.id, path: "fixture.txt" });
    listCopies.mockReturnValue(call({ positions: [position], hasMore: false }));
    listMedia.mockReturnValue(call({ media: [offline] }));

    render(
      <FileInspector
        selected={{ id: "4", name: "fixture.txt", isDir: false }}
        loading={false}
        detail={{ file, children: [], nextCursor: "", scope: 1 }}
        onRefresh={refresh}
      />,
    );

    expect(screen.queryByRole("link", { name: "Open copy" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Download" })).not.toBeInTheDocument();
  });

  it("shows directory annotations in Inspector mode", () => {
    const directory = File.create({ id: 9n, name: "Projects", mode: 2147483648n, tags: ["active"], note: "Current work" });
    render(
      <FileInspector
        selected={{ id: "9", name: "Projects", isDir: true }}
        loading={false}
        detail={{ file: directory, children: [], nextCursor: "", scope: 1 }}
        onRefresh={refresh}
      />,
    );

    expect(screen.getByText("Directory")).toBeInTheDocument();
    expect(screen.getByText("active")).toBeInTheDocument();
    expect(screen.getByText("Current work")).toBeInTheDocument();
  });

  it("uses Preview identity to scope timeline state", async () => {
    const fetchTimeline = vi.fn().mockResolvedValue({
      ok: true,
      text: async () =>
        "WEBVTT\n\n00:00:00.000 --> 00:00:30.000\ntimeline.webp#xywh=0,0,160,90\n\n00:00:30.000 --> 00:01:00.000\ntimeline.webp#xywh=160,0,160,90\n",
    });
    vi.stubGlobal("fetch", fetchTimeline);
    const file = File.create({ id: 10n, name: "walkthrough.mp4", size: 100n, hash: new Uint8Array([1]) });
    getState.mockReturnValue(
      call(
        FileStateReply.create({
          location,
          original: { fileId: file.id, locationId: 1n, observedBindingToken: "binding-a", signature: new Uint8Array([91]), sha256: file.hash, size: file.size },
        }),
      ),
    );
    const preview = PreviewManifest.create({
      generator: "video",
      fileSignature: new Uint8Array([1]),
      settingsJson: new Uint8Array([2]),
      assets: [
        { name: "poster.webp", role: "poster", mediaType: "image/webp", width: 0, height: 0 },
        { name: "timeline.webp", role: "timeline", mediaType: "image/webp", width: 320, height: 90 },
        { name: "timeline.vtt", role: "timeline-map", mediaType: "text/vtt", width: 0, height: 0 },
      ],
    });
    const { container, rerender } = render(
      <FileInspector
        selected={{ id: "10", name: "walkthrough.mp4", isDir: false }}
        loading={false}
        detail={{ file, children: [], nextCursor: "", scope: 1, preview }}
        onRefresh={refresh}
      />,
    );

    const progress = await screen.findByRole("slider", { name: "Video preview position" });
    await waitFor(() => expect(progress).toHaveAttribute("max", "60"));
    expect(screen.getByText("0:00 / 1:00")).toBeInTheDocument();
    fireEvent.change(progress, { target: { value: "49" } });
    expect(progress).toHaveValue("30");
    expect(screen.getByText("0:30 / 1:00")).toBeInTheDocument();

    const frame = container.querySelector(".file-detail-video-frame");
    const controls = container.querySelector(".file-detail-video-controls");
    expect(frame).toBeTruthy();
    expect(controls).toBeTruthy();
    expect(frame?.nextElementSibling).toBe(controls);

    // A refreshed protobuf object with the same logical identity keeps the loaded timeline.
    const samePreview = PreviewManifest.create({
      ...preview,
      assets: preview.assets.map((asset) => ({ ...asset })),
    });
    rerender(
      <FileInspector
        selected={{ id: "10", name: "walkthrough.mp4", isDir: false }}
        loading={false}
        detail={{ file, children: [], nextCursor: "", scope: 1, preview: samePreview }}
        onRefresh={refresh}
      />,
    );
    expect(fetchTimeline).toHaveBeenCalledTimes(1);
    expect(screen.getByText("0:30 / 1:00")).toBeInTheDocument();

    // Refreshing unchanged live facts must not remount the video while the request is pending.
    const checks = getEntry.mock.calls.length;
    rerender(
      <FileInspector
        selected={{ id: "10", name: "walkthrough.mp4", isDir: false }}
        loading={false}
        detail={{ file: File.create(file), children: [], nextCursor: "", scope: 1, preview: samePreview }}
        onRefresh={refresh}
      />,
    );
    await waitFor(() => expect(getEntry.mock.calls.length).toBeGreaterThan(checks));
    expect(screen.getByText("0:30 / 1:00")).toBeInTheDocument();
    expect(fetchTimeline).toHaveBeenCalledTimes(1);

    // Replacing the manifest identity remounts Preview state even for the same File.
    const posterOnly = PreviewManifest.create({
      generator: "poster-only",
      fileSignature: new Uint8Array([1]),
      settingsJson: new Uint8Array([3]),
      assets: [{ name: "poster.webp", role: "poster", mediaType: "image/webp", width: 0, height: 0 }],
    });
    rerender(
      <FileInspector
        selected={{ id: "10", name: "walkthrough.mp4", isDir: false }}
        loading={false}
        detail={{ file, children: [], nextCursor: "", scope: 1, preview: posterOnly }}
        onRefresh={refresh}
      />,
    );

    expect(screen.getByRole("slider", { name: "Video preview position" })).toBeDisabled();
    expect(screen.getByText("0:00 / 0:00")).toBeInTheDocument();
  });
});

import { MediaInspectResult } from "./media-inspect";
import { InspectMediaResponse } from "@/entity";
import { contentTime } from "./content-status";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, expect, it, vi } from "vitest";
import { Media, MediaAccess, MediaKind, Position, PositionHealth, PreviewAvailability, VolumeType } from "@/entity";
import { formatFilesize } from "@/tools";
import { MediaInspector } from "./media-inspector";

const { copies, mediaList, preview } = vi.hoisted(() => ({ copies: vi.fn(), mediaList: vi.fn(), preview: vi.fn() }));
vi.mock("@/api", async (original) => ({
  ...(await original<typeof import("@/api")>()),
  filesCli: { listCopies: copies },
  mediaCli: { list: mediaList },
  previewCli: { get: preview },
}));
const call = (value: unknown) => ({ response: Promise.resolve(value) });
const signature = new Uint8Array([1, 2]);
const position = Position.create({
  id: 7n,
  mediaId: 3n,
  path: "Camera A/photo.jpg",
  sizeBytes: 942n,
  mode: 0o644n,
  mtimeNs: 1787000000000000000n,
  writtenAtNs: 1787216640000000000n,
  sha256: new Uint8Array([9]),
  signature,
  health: PositionHealth.HEALTHY,
  checkedAtNs: 1789910806384000000n,
  healthJobId: 12n,
});
const positionRow = { id: "3:Camera A/photo.jpg", name: "photo.jpg", position, detailsAvailable: true };
const media = Media.create({
  id: 3n,
  kind: MediaKind.VOLUME,
  identity: "2ff9fb24-83d9-4a39-a32b-6434bb66d902",
  name: "Archive HDD",
  capabilities: { read: MediaAccess.CONCURRENT_RANDOM, write: MediaAccess.RANDOM },
  profile: { kind: { oneofKind: "volume", volume: { serialNumber: "REVIEW-HDD-001", type: VolumeType.HM_SMR } } },
  createdAtNs: 1789910806000000000n,
  capacityBytes: 2199023255552n,
  writtenBytes: 9562n,
  mounted: true,
  filesystemAvailableBytes: 68912308224n,
});
const mediaRow = { id: "3", name: "Archive HDD", isDir: true, isMedia: true as const, media };

beforeEach(() => {
  vi.clearAllMocks();
  copies.mockReturnValue(call({ positions: [], hasMore: false }));
  mediaList.mockReturnValue(call({ media: [] }));
  preview.mockReturnValue(call({ availability: PreviewAvailability.NOT_GENERATED, assets: [] }));
});

it("details one Position from the row the list already has", async () => {
  const { container } = render(<MediaInspector selection={positionRow} media={mediaRow} />, { wrapper: MemoryRouter });

  expect(container.querySelector(".detail-surface-preview")).toBeInTheDocument();
  expect(container.querySelector(".detail-surface-header")).toBeInTheDocument();
  expect(screen.getByTitle("photo.jpg")).toBeInTheDocument();
  expect(screen.getByText("Archive HDD · Volume")).toBeInTheDocument();
  expect(screen.getByText("Camera A/photo.jpg")).toBeInTheDocument();
  expect(screen.getByText("942 B")).toBeInTheDocument();
  expect(screen.getByText("644")).toBeInTheDocument();
  expect(screen.getByText("Modified")).toBeInTheDocument();
  expect(screen.getByText("Written")).toBeInTheDocument();
  expect(screen.getAllByText(/Last check passed/).length).toBeGreaterThan(0);
  expect(screen.getByRole("link", { name: "Check results" })).toHaveAttribute("href", "/jobs/12");
  // The content-addressed reads use the Signature the Position records, with no File identity.
  expect(preview).toHaveBeenCalledWith({ signature }, { abort: expect.any(AbortSignal) });
  await waitFor(() => expect(copies).toHaveBeenCalledWith({ signature, afterId: 0n, limit: 20 }));
  expect(await screen.findByRole("region", { name: "Archived copies" })).toBeInTheDocument();
});

it("details the selected Media", () => {
  const { container } = render(<MediaInspector selection={mediaRow} />, { wrapper: MemoryRouter });

  expect(container.querySelector(".detail-surface-header")).toBeInTheDocument();
  expect(container.querySelector(".detail-surface-preview")).not.toBeInTheDocument();
  expect(screen.getByTitle("Archive HDD")).toBeInTheDocument();
  expect(screen.getByText("2ff9fb24-83d9-4a39-a32b-6434bb66d902")).toBeInTheDocument();
  expect(screen.getByText("REVIEW-HDD-001")).toBeInTheDocument();
  expect(screen.getByText("HM-SMR")).toBeInTheDocument();
  expect(screen.getByText("concurrent random read · random write")).toBeInTheDocument();
  expect(screen.getAllByText("Online").length).toBeGreaterThan(0);
  expect(screen.getByText(formatFilesize(media.capacityBytes))).toBeInTheDocument();
  expect(screen.getByText(formatFilesize(media.writtenBytes))).toBeInTheDocument();
  // A Media has no content of its own, so nothing asks for a Preview or copies.
  expect(preview).not.toHaveBeenCalled();
  expect(copies).not.toHaveBeenCalled();
});

it("warns about a copy normal Restore will not use", () => {
  const damaged = Position.create({ ...position, health: PositionHealth.DAMAGED });
  render(<MediaInspector selection={{ ...positionRow, position: damaged }} media={mediaRow} />, { wrapper: MemoryRouter });
  expect(screen.getByText(/Not used by normal Restore/)).toBeInTheDocument();
});

it("renders ns Media dates and distinguishes an optional epoch write from an absent write", () => {
  const reply = InspectMediaResponse.create({ media, lastWrittenAtNs: 0n });
  const view = render(<MediaInspectResult reply={reply} loading={false} />);
  expect(screen.getByText(`Created ${contentTime(media.createdAtNs)}`)).toBeInTheDocument();
  expect(screen.getByText(`Last written ${contentTime(0n)}`)).toBeInTheDocument();
  view.rerender(<MediaInspectResult reply={InspectMediaResponse.create({ media })} loading={false} />);
  expect(screen.queryByText(/Last written/)).not.toBeInTheDocument();
});

it("preserves zero sentinels for unrecorded Position times", () => {
  const unchecked = Position.create({ ...position, mtimeNs: 0n, writtenAtNs: 0n, checkedAtNs: 0n });
  render(<MediaInspector selection={{ ...positionRow, position: unchecked }} media={mediaRow} />, { wrapper: MemoryRouter });
  expect(screen.getByText(/Not checked/)).toBeInTheDocument();
  expect(screen.getAllByText("Date unknown")).toHaveLength(2);
});

it("asks for nothing when nothing is selected", () => {
  render(<MediaInspector />, { wrapper: MemoryRouter });
  expect(screen.getByText("Select a Media or Position")).toBeInTheDocument();
  expect(preview).not.toHaveBeenCalled();
  expect(copies).not.toHaveBeenCalled();
});

it("states that a Position without a Signature cannot match its copies", () => {
  const unsigned = Position.create({ ...position, signature: new Uint8Array() });
  render(<MediaInspector selection={{ ...positionRow, position: unsigned }} media={mediaRow} />, { wrapper: MemoryRouter });
  expect(screen.getByText(/records no Signature/)).toBeInTheDocument();
  expect(preview).not.toHaveBeenCalled();
  expect(copies).not.toHaveBeenCalled();
});

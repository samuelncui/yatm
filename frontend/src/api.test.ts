import { describe, expect, it } from "vitest";

import { convertFiles, convertSearchResults, convertMedia, convertPositions, MODE_DIR } from "@/api";
import { File, Media, MediaAccess, MediaKind, Position, VolumeType } from "@/entity";

describe("Library row status", () => {
  it("projects supplied summaries to small accessible dots for folder and search rows", () => {
    const file = File.create({
      id: 1n,
      name: "notes.txt",
      contentSummary: {
        hasOriginal: true,
        signatureKnown: true,
        currentObservationValid: true,
        originalAvailability: 1,
        hasVersions: true,
        restorableVersionCopies: 1n,
        archivedCopies: 0n,
      },
    });
    const result = convertFiles([file])[0];
    expect(result.status).toMatchObject({ color: "#15803d", label: expect.stringContaining("Changes not backed up") });
    expect(convertSearchResults([{ file, path: "/docs/notes.txt" }])[0].status).toEqual(result.status);
    expect(convertFiles([File.create({ id: 2n, mode: MODE_DIR, contentSummary: file.contentSummary })])[0].status).toBeUndefined();
    expect(convertFiles([File.create({ id: 3n })])[0].status).toBeUndefined();
  });
});

describe("convertMedia", () => {
  it("shows access and online-read behavior", () => {
    const volume = Media.create({
      id: 1n,
      kind: MediaKind.VOLUME,
      identity: "11111111-1111-1111-1111-111111111111",
      name: "Offline Disk",
      capabilities: { read: MediaAccess.CONCURRENT_RANDOM, write: MediaAccess.SEQUENTIAL },
      profile: { kind: { oneofKind: "volume", volume: { serialNumber: "disk", type: VolumeType.HM_SMR } } },
    });
    const tape = Media.create({
      id: 2n,
      kind: MediaKind.TAPE,
      identity: "ABC001",
      capabilities: { read: MediaAccess.SEQUENTIAL, write: MediaAccess.SEQUENTIAL },
      profile: { kind: { oneofKind: "tape", tape: { serialNumber: "", encryption: "key", format: "ltfs_v1" } } },
    });

    expect(convertMedia([volume])[0]?.name).toBe("Offline Disk");
    expect(convertMedia([volume])[0]?.details).toContain("HM-SMR · concurrent random read · sequential write · Mount required");
    volume.mounted = true;
    expect(convertMedia([volume])[0]?.details).toContain("HM-SMR · concurrent random read · sequential write · Online");
    expect(convertMedia([tape])[0]?.name).toBe("ABC001");
    expect(convertMedia([tape])[0]?.details).toContain("Tape · sequential read · sequential write · Restore required");
  });
});

describe("convertPositions", () => {
  it("exposes physical content without inventing a Library File owner", () => {
    const linked = Position.create({ id: 42n, mediaId: 3n, path: "linked.txt", signature: new Uint8Array([0, 255]) });
    const unlinked = Position.create({ mediaId: 3n, path: "unlinked.txt" });
    const directory = Position.create({ mediaId: 3n, path: "folder/", mode: MODE_DIR });

    const [linkedFile, unlinkedFile, directoryFile] = convertPositions([linked, unlinked, directory]);

    expect(linkedFile).toMatchObject({ positionID: 42n, signature: linked.signature, detailsAvailable: true, openable: true });
    expect(unlinkedFile).toMatchObject({ detailsAvailable: true, openable: true });
    expect(unlinkedFile).not.toHaveProperty("libraryFileId");
    expect(directoryFile).toMatchObject({ detailsAvailable: false, openable: true });
  });
});

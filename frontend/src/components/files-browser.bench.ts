import { bench, expect, vi } from "vitest";
import { EntryKind, FileScope, FilesEntry, ListFilesResponse } from "@/entity";
import { listingStream } from "@/test/files-fixture";
import { libraryDirectoryReference, listDirectory } from "./files-browser";

const count = 10_000;
// Construct inputs once, outside the measured operations. RPC replies are immediate.
const entries = Array.from({ length: count }, (_, index) =>
  FilesEntry.create({
    reference: libraryDirectoryReference(String(index + 1)),
    name: `file-${index + 1}.txt`,
    kind: EntryKind.FILE,
    sizeBytes: BigInt(index + 1),
  }),
);
const batches = Array.from({ length: count / 500 }, (_, index) =>
  ListFilesResponse.create({
    entries: entries.slice(index * 500, (index + 1) * 500),
    totalEntryCount: index === 0 ? BigInt(count) : undefined,
    scope: FileScope.ALL,
  }),
);
const directory = libraryDirectoryReference("0");

// Avoid retaining mock call history across benchmark iterations.
vi.mock("@/api", () => ({
  filesCli: { list: () => listingStream(...batches) },
}));

bench(
  "stream and accumulate 10k directory rows in 500-row batches",
  async () => {
    let received = 0;
    // Use the common callback argument; older sources do not supply the accumulator.
    const listing = await listDirectory(directory, FileScope.ALL, (batch) => (received += batch.files.length));
    expect(received).toBe(count);
    expect(listing.total).toBe(BigInt(count));
    expect(listing.files).toHaveLength(count);
    expect(listing.files.at(-1)?.id).toBe(String(count));
  },
  { time: 250, iterations: 1, warmupTime: 50, warmupIterations: 1 },
);

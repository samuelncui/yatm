import { expect, it, vi } from "vitest";
import { FileVersion } from "@/entity";
import { versionArchiveTime } from "./content-status";
import { compareVersionDates } from "./use-file-versions";

const { versions } = vi.hoisted(() => ({ versions: vi.fn() }));
vi.mock("@/api", () => ({ filesCli: { listVersions: versions } }));

it("orders archive instants by exact ns, then ID, with undated versions after negative and epoch dates", () => {
  const versions = [
    FileVersion.create({ id: 9n }),
    FileVersion.create({ id: 8n, lastArchivedAtNs: -1n }),
    FileVersion.create({ id: 7n, lastArchivedAtNs: 0n }),
    FileVersion.create({ id: 6n, lastArchivedAtNs: 1700000000000000001n }),
    FileVersion.create({ id: 1n, lastArchivedAtNs: 1700000000000000002n }),
    FileVersion.create({ id: 2n, lastArchivedAtNs: 1700000000000000002n }),
  ];
  expect(versions.sort(compareVersionDates).map((version) => version.id)).toEqual([2n, 1n, 6n, 7n, 8n, 9n]);
  expect(versionArchiveTime(FileVersion.create({ firstArchivedAtNs: -1n, lastArchivedAtNs: 0n }))).toBe(0n);
  expect(versionArchiveTime(FileVersion.create())).toBeUndefined();
});

it("orders first-only archive dates as unknown last dates while retaining their display value", () => {
  const firstOnly = FileVersion.create({ id: 2n, firstArchivedAtNs: 1700000000000000002n });
  const dated = FileVersion.create({ id: 1n, lastArchivedAtNs: 1700000000000000001n });
  const undated = FileVersion.create({ id: 3n });
  expect([firstOnly, dated, undated].sort(compareVersionDates)).toEqual([dated, undated, firstOnly]);
  expect(versionArchiveTime(firstOnly)).toBe(firstOnly.firstArchivedAtNs);
});

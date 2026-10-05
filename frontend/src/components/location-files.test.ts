import { afterEach, describe, expect, it, vi } from "vitest";
import { streamListingLater } from "@/test/files-fixture";
import { File, FileScope, Location, LocationEntry, LocationEntryRef, GetLocationResponse, ListFilesResponse } from "@/entity";
import { filesCli, locationCli } from "@/api";
import { associatedLibraryFileID, locatedFile, selectionForFile, locationEntryFile, locationFilePage } from "./location-files";

afterEach(() => vi.restoreAllMocks());

describe("Concurrent live directory reads", () => {
  it("shares identical in-flight pages without serializing independent directories or caching observations", async () => {
    vi.spyOn(locationCli, "get").mockImplementation(({ id }) => ({ response: Promise.resolve(GetLocationResponse.create({ location: { id } })) }) as any);
    const pending: (() => void)[] = [];
    // A plain directory read streams one listing; a name filter is a query and pages.
    const reads = { calls: 0 };
    const deferred = () => new Promise<ListFilesResponse>((resolve) => pending.push(() => resolve(ListFilesResponse.create())));
    vi.spyOn(filesCli, "list").mockImplementation(() => {
      reads.calls++;
      return streamListingLater(deferred()) as any;
    });
    vi.spyOn(filesCli, "search").mockImplementation(() => {
      reads.calls++;
      return { response: deferred() } as any;
    });
    const first = locationFilePage("4", "camera");
    expect(locationFilePage("4", "camera/")).toBe(first);
    const pages = [
      first,
      locationFilePage("5", "camera"),
      locationFilePage("4", "exports"),
      locationFilePage("4", "camera", "next"),
      locationFilePage("4", "camera", "", "image"),
    ];
    await vi.waitFor(() => expect(reads.calls).toBe(5));
    pending[0]();
    pending[1]();
    for (let index = 2; index < 5; index++) {
      pending[index]();
    }
    await Promise.all(pages);

    const refreshed = locationFilePage("4", "camera");
    expect(refreshed).not.toBe(first);
    await vi.waitFor(() => expect(reads.calls).toBe(6));
    pending.at(-1)!();
    await refreshed;
  });

  it("releases failed reads so retry can observe a recovered directory", async () => {
    vi.spyOn(locationCli, "get").mockImplementation(({ id }) => ({ response: Promise.resolve(GetLocationResponse.create({ location: { id } })) }) as any);
    const list = vi.spyOn(filesCli, "list").mockImplementationOnce(() => streamListingLater(Promise.reject(new Error("Unavailable"))) as any);
    const first = locationFilePage("4", "");
    expect(locationFilePage("4", "")).toBe(first);
    await expect(first).rejects.toThrow("Unavailable");
    list.mockImplementationOnce(() => streamListingLater(Promise.resolve(ListFilesResponse.create())) as any);
    await expect(locationFilePage("4", "")).resolves.toMatchObject({ files: [] });
    expect(list).toHaveBeenCalledTimes(2);
  });

  it("does not propagate one directory failure to independent reads or trigger collection", async () => {
    vi.spyOn(locationCli, "get").mockImplementation(({ id }) => ({ response: Promise.resolve(GetLocationResponse.create({ location: { id } })) }) as any);
    let reject!: (error: Error) => void;
    const list = vi
      .spyOn(filesCli, "list")
      .mockImplementationOnce(
        () =>
          streamListingLater(
            new Promise<ListFilesResponse>((_, fail) => {
              reject = fail;
            }),
          ) as any,
      )
      .mockImplementationOnce(() => streamListingLater(Promise.resolve(ListFilesResponse.create())) as any);
    const failed = expect(locationFilePage("4", "unavailable")).rejects.toThrow("Unavailable");
    const queued = locationFilePage("4", "available");
    await vi.waitFor(() => expect(list).toHaveBeenCalledTimes(2));
    reject(new Error("Unavailable"));
    await failed;
    await expect(queued).resolves.toMatchObject({ files: [] });
    expect(list).toHaveBeenCalledTimes(2);
  });
});

describe("Indexed selection adaptation", () => {
  it("preserves the physical binding and clones its selection", () => {
    const file = locatedFile({ id: "7", name: "logical.txt" }, 4n, "directory/physical.txt");
    const selection = selectionForFile(file, FileScope.SAVED);
    expect(selection.target).toEqual({ oneofKind: "location", location: { locationId: 4n, path: "directory/physical.txt" } });
    expect(selection).not.toBe(file.originSelection);
  });
  it("does not silently reinterpret malformed physical rows as Library selections", () => {
    expect(() => selectionForFile({ id: "7", name: "file", physicalLocationID: "4" }, FileScope.ALL)).toThrow("no live selection");
    expect(() => selectionForFile({ id: "7", name: "file", originSelection: { locationId: "4" } }, FileScope.ALL)).toThrow("invalid selection");
  });
  it("represents a live file without inventing a Library ID or content state", () => {
    const reference = LocationEntryRef.create({
      locationId: 4n,
      path: "docs/未收录.txt",
      facts: { sizeBytes: 64n, mtimeNs: 1000000n, mode: 420 },
    });
    const file = locationEntryFile(LocationEntry.create({ path: reference.path, reference }), Location.create({ id: 4n, name: "Documents" }));
    expect(file.id).toBe("location-file:4:docs/未收录.txt");
    expect(file.size).toBe(64);
    expect(file.status).toBeUndefined();
    expect(selectionForFile(file, FileScope.SAVED).target).toMatchObject({ oneofKind: "location", location: { locationId: 4n, path: reference.path } });
  });
  it("keeps physical row identity stable when admission or observed content changes", () => {
    const location = Location.create({ id: 4n });
    const entry = LocationEntry.create({ path: "image.jpg", reference: { locationId: 4n, path: "image.jpg" } });
    const initial = locationEntryFile(entry, location);
    const admitted = locationEntryFile({ ...entry, file: File.create({ id: 19n }) }, location);
    expect(admitted.id).toBe(initial.id);
    expect(associatedLibraryFileID(initial)).toBeUndefined();
    expect(associatedLibraryFileID(admitted)).toBe("19");
    // Identity is the Location and relative path, so an admitted row keeps its target.
    expect(admitted.locationReference).toEqual(expect.objectContaining({ locationId: 4n, path: "image.jpg" }));
  });
});

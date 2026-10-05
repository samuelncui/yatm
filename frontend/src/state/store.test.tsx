import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { FileScope, FileSelection, FileVersion, Job } from "@/entity";
import { AppStateProvider, useSelections } from "./react";
import { createAppStore } from "./store";
import {
  addSelectionEntries,
  encodeSelection,
  makeSelectEntries,
  selectionActions,
  selectionStorageKey,
  saveSelectionEntries,
  type SelectionEntry,
} from "./selections";
import { encodeJobList, jobListActions, makeSelectJobList } from "./jobs";
import { readStored, writeStored, removeStored, stringCodec } from "./storage";

const entry = (id = 7n): SelectionEntry => ({
  key: `version:${id}`,
  name: "photo.jpg",
  path: "Pictures/photo.jpg",
  fileID: "4",
  version: FileVersion.create({ id, fileId: 4n, signature: new Uint8Array([0, 127, 255]), sizeBytes: 9007199254740993n }),
});
const List = ({ name }: { name: string }) => {
  const selection = useSelections("restore");
  return (
    <section aria-label={name}>
      <output>
        {name}:{selection.entries.map((value) => value.key).join(",")}
      </output>
      <button onClick={() => selection.merge([entry()])}>Add {name}</button>
    </section>
  );
};
beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
  vi.restoreAllMocks();
});

it("updates mounted views immediately while keeping separate application stores isolated", () => {
  const first = createAppStore();
  const second = createAppStore();
  render(
    <>
      <AppStateProvider store={first}>
        <List name="details" />
        <List name="creation" />
      </AppStateProvider>
      <AppStateProvider store={second}>
        <List name="other" />
      </AppStateProvider>
    </>,
  );
  fireEvent.click(screen.getByText("Add details"));
  expect(screen.getByText("creation:version:7")).toBeVisible();
  expect(screen.getByText("other:")).toBeVisible();
  fireEvent.click(screen.getByText("Add creation"));
  expect(first.getState().selections.restore.revision).toBe(1);
});

it("hydrates once and round-trips protobuf IDs, bytes and oneofs without nonserializable actions", () => {
  const selection = FileSelection.create({ scope: FileScope.ALL, target: { oneofKind: "location", location: { locationId: 9007199254740993n, path: "a" } } });
  saveSelectionEntries("restore", [entry(), { key: FileSelection.toJsonString(selection), name: "a", path: "a", selection }]);
  const errors = vi.spyOn(console, "error");
  const store = createAppStore();
  const select = makeSelectEntries("restore");
  const values = select(store.getState());
  expect(values[0].version).toEqual(entry().version);
  expect(values[1].selection).toEqual(selection);
  sessionStorage.setItem(selectionStorageKey("restore"), "[]");
  addSelectionEntries(store, "archive", [entry(8n)]);
  expect(select(store.getState())).toBe(values);
  expect(createAppStore().getState().selections.restore.entries).toEqual([]);
  expect(errors).not.toHaveBeenCalled();
});

it("clears only a submitted revision and preserves additions and failed submissions", () => {
  const store = createAppStore();
  store.dispatch(selectionActions.replaced({ kind: "restore", entries: [encodeSelection(entry())] }));
  const submitted = store.getState().selections.restore.revision;
  // A failed request dispatches no success action.
  expect(makeSelectEntries("restore")(store.getState())).toEqual([entry()]);
  addSelectionEntries(store, "restore", [entry(8n)]);
  store.dispatch(selectionActions.submitted({ kind: "restore", revision: submitted }));
  expect(store.getState().selections.restore.entries).toHaveLength(2);
  store.dispatch(selectionActions.submitted({ kind: "restore", revision: store.getState().selections.restore.revision }));
  expect(store.getState().selections.restore.entries).toEqual([]);
  expect(sessionStorage.getItem(selectionStorageKey("restore"))).toBeNull();
});

it("keeps in-memory additions and successful clearing when storage fails", () => {
  sessionStorage.setItem(selectionStorageKey("restore"), "not-json");
  const store = createAppStore();
  expect(store.getState().selections.restore.entries).toEqual([]);
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new Error("Quota exceeded");
  });
  vi.spyOn(Storage.prototype, "removeItem").mockImplementation(() => {
    throw new Error("Storage denied");
  });
  addSelectionEntries(store, "restore", [entry()]);
  expect(store.getState().selections.restore.entries).toHaveLength(1);
  expect(() => store.dispatch(selectionActions.submitted({ kind: "restore", revision: 1 }))).not.toThrow();
  expect(store.getState().selections.restore.entries).toEqual([]);
  expect(writeStored("local", "directory", "chosen", stringCodec)).toBe(false);
  expect(removeStored("local", "directory")).toBe(false);
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
    throw new Error("Storage denied");
  });
  expect(readStored("local", "directory", stringCodec)).toBeUndefined();
});

it("retains filter-specific Jobs snapshots without decoding rows on scroll or unrelated changes", () => {
  const store = createAppStore();
  const snapshot = {
    jobs: [Job.create({ id: 9007199254740993n, revision: 3n })],
    revision: 4n,
    snapshotRevision: 2n,
    beforeID: 1n,
    hasMore: true,
    scrollTop: 0,
  };
  store.dispatch(jobListActions.saved({ key: "archive", snapshot: encodeJobList(snapshot) }));
  const select = makeSelectJobList("archive");
  const initial = select(store.getState())!;
  expect(initial).toEqual(snapshot);
  store.dispatch(jobListActions.scrolled({ key: "archive", scrollTop: 220 }));
  expect(select(store.getState())!.jobs).toBe(initial.jobs);
  expect(select(store.getState())!.scrollTop).toBe(220);
  act(() => addSelectionEntries(store, "archive", [entry()]));
  expect(select(store.getState())!.jobs).toBe(initial.jobs);
  expect(makeSelectJobList("restore")(store.getState())).toBeUndefined();
});

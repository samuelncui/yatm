import { useEffect, type ReactNode } from "react";
import { screen, waitFor, within } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, expect, it, vi } from "vitest";
import { EntryKind, FileOperationKind, IdenticalGroup, IdenticalMember, IdenticalSortKey, IdenticalSortOrder, LibrarySettings } from "@/entity";
import { IdenticalFiles } from "./identical";

const api = vi.hoisted(() => ({ find: vi.fn(), rows: vi.fn(), close: vi.fn(), lookup: vi.fn() }));
beforeEach(() => vi.clearAllMocks());
vi.mock("@/api", () => ({
  filesCli: {
    findIdentical: api.find,
    listIdenticalRows: api.rows,
    lookupIdenticalPositions: api.lookup,
    closeIdenticalResult: api.close,
  },
  settingsCli: {
    get: () => ({ response: Promise.resolve({ value: { value: { oneofKind: "library", library: LibrarySettings.create({ confirmRemove: false }) } } }) }),
  },
}));
vi.mock("@/components/file-metadata-dialog", () => ({ FileMetadataDialog: () => null }));
vi.mock("./file-detail", () => ({ FileInspector: ({ name }: { name?: string }) => <aside aria-label="Inspector">{name ?? "No selection"}</aside> }));

// Use the installed Chonky browser, toolbar, selection store and file rows.
// Only the virtual viewport is replaced because jsdom has no layout geometry.
vi.mock("react-virtuoso", () => ({
  Virtuoso: ({
    totalCount,
    itemContent,
    rangeChanged,
  }: {
    totalCount: number;
    itemContent: (index: number) => ReactNode;
    rangeChanged?: (range: { startIndex: number; endIndex: number }) => void;
  }) => {
    useEffect(() => {
      if (totalCount) rangeChanged?.({ startIndex: 0, endIndex: totalCount - 1 });
    }, [rangeChanged, totalCount]);
    return (
      <div>
        {Array.from({ length: totalCount }, (_, index) => (
          <div key={index}>{itemContent(index)}</div>
        ))}
      </div>
    );
  },
}));

it("shows the page-owned hidden-file control and follows real Chonky keyboard selection", async () => {
  const group = IdenticalGroup.create({ id: "1", name: "Related", memberCount: 2n, fingerprint: "snapshot" });
  const member = (id: bigint) =>
    IdenticalMember.create({
      entry: {
        reference: { target: { oneofKind: "fileId", fileId: id } },
        associatedFileId: id,
        name: "copy-" + id + ".txt",
        path: "copy-" + id + ".txt",
        kind: EntryKind.FILE,
        operations: [FileOperationKind.UPDATE_METADATA],
      },
    });
  api.find.mockReturnValue({ response: Promise.resolve({ resultId: "result", allRowCount: 3n, visibleRowCount: 3n }) });
  api.rows.mockReturnValue({
    response: Promise.resolve({
      rows: [
        { position: 0n, group, groupHeaderPosition: 0n, displayMemberCount: 2n },
        { position: 1n, group, groupHeaderPosition: 0n, displayMemberCount: 2n, member: member(1n) },
        { position: 2n, group, groupHeaderPosition: 0n, displayMemberCount: 2n, member: member(2n) },
      ],
      totalRowCount: 3n,
    }),
  });
  api.close.mockReturnValue({ response: Promise.resolve({}) });
  render(
    <MemoryRouter>
      <IdenticalFiles />
    </MemoryRouter>,
  );
  await userEvent.click(screen.getByRole("button", { name: "Find" }));
  const pane = screen.getByRole("region", { name: "Identical file groups" });
  const selection = await within(pane).findByRole("checkbox", { name: "Select copy-1.txt" });
  expect(screen.getByRole("switch", { name: "Show hidden files" })).toBeInTheDocument();
  expect(within(pane).queryByRole("button", { name: "Show hidden files" })).not.toBeInTheDocument();
  selection.focus();
  await userEvent.keyboard(" ");
  await waitFor(() => expect(screen.getByRole("complementary", { name: "Inspector" })).toHaveTextContent("copy-1.txt"));
});

it("keeps real Chonky selection changes while sort positions and pages are loading", async () => {
  const group = IdenticalGroup.create({ id: "group", name: "Related", memberCount: 2n });
  const row = (id: bigint, position: bigint) => ({
    position,
    group,
    groupHeaderPosition: 0n,
    displayMemberCount: 2n,
    member: IdenticalMember.create({
      evidence: [],
      entry: {
        reference: { target: { oneofKind: "fileId", fileId: id } },
        associatedFileId: id,
        name: `copy-${id}.txt`,
        path: `copy-${id}.txt`,
        kind: EntryKind.FILE,
        sizeBytes: id,
      },
    }),
  });
  const header = { position: 0n, group, groupHeaderPosition: 0n, displayMemberCount: 2n };
  const reply = (value: unknown) => ({ response: Promise.resolve(value) });
  api.find.mockReturnValue(reply({ resultId: "result", allRowCount: 3n, visibleRowCount: 3n, groupCount: 1n }));
  let finishPage!: (value: unknown) => void;
  const sortedPage = new Promise((resolve) => {
    finishPage = resolve;
  });
  api.rows.mockImplementation(({ sortKey }: { sortKey: IdenticalSortKey }) =>
    sortKey === IdenticalSortKey.SIZE ? { response: sortedPage } : reply({ rows: [header, row(1n, 1n), row(2n, 2n)], totalRowCount: 3n }),
  );
  api.lookup.mockImplementation(({ fileIds }: { fileIds: bigint[] }) =>
    reply({
      positions: fileIds.map((id) => ({
        fileId: id,
        groupId: group.id,
        allPosition: 3n - id,
        visiblePosition: 3n - id,
        allGroupHeaderPosition: 0n,
        visibleGroupHeaderPosition: 0n,
      })),
    }),
  );
  let finishLookup!: (value: unknown) => void;
  api.lookup.mockReturnValueOnce({
    response: new Promise((resolve) => {
      finishLookup = resolve;
    }),
  });
  render(
    <MemoryRouter>
      <IdenticalFiles />
    </MemoryRouter>,
  );
  await userEvent.click(screen.getByRole("button", { name: "Find" }));
  const pane = screen.getByRole("region", { name: "Identical file groups" });
  await userEvent.click(await within(pane).findByRole("checkbox", { name: "Select copy-1.txt" }));
  await waitFor(() => expect(screen.getByLabelText("Inspector")).toHaveTextContent("copy-1.txt"));
  await userEvent.click(screen.getByRole("button", { name: "Options" }));
  await userEvent.click(screen.getByRole("menuitem", { name: "Sort by size" }));
  await waitFor(() => expect(api.lookup).toHaveBeenCalledTimes(1));
  await userEvent.click(within(pane).getByRole("checkbox", { name: "Select copy-2.txt" }));
  finishLookup({
    positions: [{ fileId: 1n, groupId: group.id, allPosition: 2n, visiblePosition: 2n, allGroupHeaderPosition: 0n, visibleGroupHeaderPosition: 0n }],
  });
  await waitFor(() =>
    expect(api.rows).toHaveBeenCalledWith(expect.objectContaining({ sortKey: IdenticalSortKey.SIZE, sortOrder: IdenticalSortOrder.DESC }), expect.anything()),
  );
  expect(api.lookup).toHaveBeenCalledWith(expect.objectContaining({ fileIds: [1n], sortKey: IdenticalSortKey.SIZE }));
  expect(pane.querySelectorAll('[data-chonky-file-id="1"]')).toHaveLength(1);
  expect(pane.querySelectorAll('[data-chonky-file-id="2"]')).toHaveLength(1);
  expect(screen.getByLabelText("Inspector")).toHaveTextContent("copy-2.txt");
  expect(within(pane).getByText(/2 selected/)).toBeInTheDocument();
  finishPage({ rows: [header, row(2n, 1n), row(1n, 2n)], totalRowCount: 3n });
  await within(pane).findAllByTitle("copy-2.txt");
  expect(pane.querySelectorAll('[data-chonky-file-id="1"]')).toHaveLength(1);
  expect(screen.getByLabelText("Inspector")).toHaveTextContent("copy-2.txt");
  expect(within(pane).getByText(/2 selected/)).toBeInTheDocument();
  expect(api.find).toHaveBeenCalledTimes(1);
});

it("keeps the selected file and Inspector when another group collapses", async () => {
  const other = IdenticalGroup.create({ id: "other", name: "Other group", memberCount: 1n });
  const selected = IdenticalGroup.create({ id: "selected", name: "Selected group", memberCount: 1n });
  const rows = [other, selected].flatMap((group, index) => {
    const header = { position: BigInt(index * 2), group, groupHeaderPosition: BigInt(index * 2), displayMemberCount: 1n };
    const id = BigInt(index + 1);
    return [
      header,
      {
        ...header,
        position: header.position + 1n,
        member: IdenticalMember.create({
          entry: {
            reference: { target: { oneofKind: "fileId", fileId: id } },
            associatedFileId: id,
            name: `${group.id}.txt`,
            kind: EntryKind.FILE,
          },
        }),
      },
    ];
  });
  api.find.mockReturnValue({ response: Promise.resolve({ resultId: "result", allRowCount: 4n, visibleRowCount: 4n }) });
  api.rows.mockReturnValue({ response: Promise.resolve({ rows, totalRowCount: 4n }) });
  api.close.mockReturnValue({ response: Promise.resolve({}) });
  render(
    <MemoryRouter>
      <IdenticalFiles />
    </MemoryRouter>,
  );
  await userEvent.click(screen.getByRole("button", { name: "Find" }));
  const pane = screen.getByRole("region", { name: "Identical file groups" });
  await userEvent.click(await within(pane).findByRole("checkbox", { name: "Select selected.txt" }));
  expect(screen.getByLabelText("Inspector")).toHaveTextContent("selected.txt");
  await userEvent.click(within(pane).getByRole("button", { name: "Other group" }));
  await waitFor(() => expect(within(pane).queryByRole("checkbox", { name: "Select other.txt" })).not.toBeInTheDocument());
  expect(within(pane).getByRole("checkbox", { name: "Select selected.txt" })).toBeChecked();
  expect(screen.getByLabelText("Inspector")).toHaveTextContent("selected.txt");
  expect(within(pane).getByText(/1 selected/)).toBeInTheDocument();
  expect(api.find).toHaveBeenCalledTimes(1);
});

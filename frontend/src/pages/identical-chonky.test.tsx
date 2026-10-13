import { useEffect, useRef, useState, type ReactNode } from "react";
import { act, screen, waitFor, within } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, expect, it, vi } from "vitest";
import { EntryKind, FileOperationKind, IdenticalGroup, IdenticalMember, IdenticalSortKey, IdenticalSortOrder, LibrarySettings } from "@/entity";
import { IdenticalFiles } from "./identical";

const api = vi.hoisted(() => ({ find: vi.fn(), rows: vi.fn(), close: vi.fn(), lookup: vi.fn() }));
beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
});
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
    const [start, setStart] = useState(0);
    const end = Math.min(start + 19, totalCount - 1);
    const callback = useRef(rangeChanged);
    callback.current = rangeChanged;
    useEffect(() => {
      if (totalCount) callback.current?.({ startIndex: start, endIndex: end });
    }, [start, end, totalCount]);
    return (
      <div data-testid="virtual-viewport" data-total-count={totalCount}>
        <button onClick={() => setStart(0)}>Jump to row 0</button>
        <button onClick={() => setStart(240)}>Jump to row 240</button>
        <button onClick={() => setStart(740)}>Jump to row 740</button>
        {Array.from({ length: Math.max(0, end - start + 1) }, (_, offset) => (
          <div key={start + offset}>{itemContent(start + offset)}</div>
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
  const row = await within(pane).findByRole("listitem", { name: "copy-1.txt" });
  expect(screen.getByRole("switch", { name: "Show hidden files" })).toBeInTheDocument();
  expect(within(pane).queryByRole("button", { name: "Show hidden files" })).not.toBeInTheDocument();
  expect(within(pane).queryByRole("checkbox")).not.toBeInTheDocument();
  row.focus();
  expect(row).toHaveFocus();
  await userEvent.keyboard(" ");
  await waitFor(() => expect(screen.getByRole("complementary", { name: "Inspector" })).toHaveTextContent("copy-1.txt"));
  expect(within(pane).getByText(/1 selected/)).toBeInTheDocument();
});

it("keeps real Chonky selection changes while sort positions and pages are loading", async () => {
  const user = userEvent.setup();
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
  api.rows.mockImplementation(({ sortOrder }: { sortOrder: IdenticalSortOrder }) =>
    sortOrder === IdenticalSortOrder.ASC ? { response: sortedPage } : reply({ rows: [header, row(1n, 1n), row(2n, 2n)], totalRowCount: 3n }),
  );
  api.lookup.mockImplementation(({ fileIds }: { fileIds: bigint[] }) =>
    reply({
      positions: fileIds.map((id) => ({
        fileId: id,
        groupId: group.id,
        allPosition: id,
        visiblePosition: id,
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
  await user.click(screen.getByRole("button", { name: "Find" }));
  const pane = screen.getByRole("region", { name: "Identical file groups" });
  await user.click(await within(pane).findByRole("listitem", { name: "copy-1.txt" }));
  await waitFor(() => expect(screen.getByLabelText("Inspector")).toHaveTextContent("copy-1.txt"));
  await user.click(screen.getByRole("button", { name: "Options" }));
  await user.click(screen.getByRole("menuitem", { name: "Sort by size" }));
  await waitFor(() => expect(api.lookup).toHaveBeenCalledTimes(1));
  await user.keyboard("{Control>}");
  await user.click(within(pane).getByRole("listitem", { name: "copy-2.txt" }));
  await user.keyboard("{/Control}");
  finishLookup({
    positions: [{ fileId: 1n, groupId: group.id, allPosition: 1n, visiblePosition: 1n, allGroupHeaderPosition: 0n, visibleGroupHeaderPosition: 0n }],
  });
  await waitFor(() =>
    expect(api.rows).toHaveBeenCalledWith(expect.objectContaining({ sortKey: IdenticalSortKey.SIZE, sortOrder: IdenticalSortOrder.ASC }), expect.anything()),
  );
  expect(api.lookup).toHaveBeenCalledWith(expect.objectContaining({ fileIds: [1n], sortKey: IdenticalSortKey.SIZE }));
  expect(pane.querySelectorAll('[data-chonky-file-id="1"]')).toHaveLength(1);
  expect(pane.querySelectorAll('[data-chonky-file-id="2"]')).toHaveLength(1);
  expect(screen.getByLabelText("Inspector")).toHaveTextContent("copy-2.txt");
  expect(within(pane).getByText(/2 selected/)).toBeInTheDocument();
  await act(async () => finishPage({ rows: [header, row(1n, 1n), row(2n, 2n)], totalRowCount: 3n }));
  await within(pane).findByRole("listitem", { name: "copy-2.txt" });
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
  await userEvent.click(await within(pane).findByRole("listitem", { name: "selected.txt" }));
  expect(screen.getByLabelText("Inspector")).toHaveTextContent("selected.txt");
  await userEvent.click(within(pane).getByRole("button", { name: "Other group" }));
  await waitFor(() => expect(within(pane).queryByRole("listitem", { name: "other.txt" })).not.toBeInTheDocument());
  expect(within(pane).getByRole("listitem", { name: "selected.txt" })).toBeInTheDocument();
  expect(screen.getByLabelText("Inspector")).toHaveTextContent("selected.txt");
  expect(within(pane).getByText(/1 selected/)).toBeInTheDocument();
  expect(api.find).toHaveBeenCalledTimes(1);
});

it("sorts an unloaded range globally without replacing the visible rows with placeholders", async () => {
  const rows = (offset: bigint, descending: boolean) =>
    Array.from({ length: Math.min(100, Number(999n - offset)) }, (_, index) => {
      const position = offset + BigInt(index);
      const groupNumber = descending ? 333n - position / 3n : position / 3n + 1n;
      const group = IdenticalGroup.create({ id: String(groupNumber), name: `Group ${groupNumber}`, memberCount: 2n });
      const header = { position, group, groupHeaderPosition: position - (position % 3n), displayMemberCount: 2n };
      if (position % 3n === 0n) return header;
      const id = groupNumber * 2n - 2n + (position % 3n);
      return {
        ...header,
        member: IdenticalMember.create({
          entry: {
            reference: { target: { oneofKind: "fileId", fileId: id } },
            associatedFileId: id,
            name: `copy-${String(id).padStart(3, "0")}.txt`,
            path: `copy-${String(id).padStart(3, "0")}.txt`,
            sizeBytes: groupNumber,
            kind: EntryKind.FILE,
          },
        }),
      };
    });
  api.find.mockReturnValue({ response: Promise.resolve({ resultId: "result", allRowCount: 999n, visibleRowCount: 999n }) });
  api.close.mockReturnValue({ response: Promise.resolve({}) });
  let finish!: () => void;
  let pendingSort = true;
  api.rows.mockImplementation(({ offset, sortOrder }: { offset: bigint; sortOrder: IdenticalSortOrder }) => ({
    response:
      sortOrder === IdenticalSortOrder.ASC && pendingSort
        ? new Promise((resolve) => {
            finish = () => {
              pendingSort = false;
              resolve({ rows: rows(offset, false), totalRowCount: 999n });
            };
          })
        : Promise.resolve({ rows: rows(offset, sortOrder === IdenticalSortOrder.DESC), totalRowCount: 999n }),
  }));
  render(
    <MemoryRouter>
      <IdenticalFiles />
    </MemoryRouter>,
  );
  await userEvent.click(screen.getByRole("button", { name: "Find" }));
  const pane = screen.getByRole("region", { name: "Identical file groups" });
  await within(pane).findByRole("listitem", { name: "copy-665.txt" });
  expect(api.rows).toHaveBeenCalledWith(expect.objectContaining({ sortKey: IdenticalSortKey.SIZE, sortOrder: IdenticalSortOrder.DESC }), expect.anything());
  await userEvent.click(screen.getByRole("button", { name: "Jump to row 240" }));
  await within(pane).findByRole("listitem", { name: "copy-505.txt" });
  await userEvent.click(screen.getByRole("button", { name: "Options" }));
  expect(screen.queryByRole("menuitem", { name: "Sort by name" })).not.toBeInTheDocument();
  expect(screen.queryByRole("menuitem", { name: "Sort by date" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("menuitem", { name: "Sort by size" }));
  await waitFor(() =>
    expect(api.rows).toHaveBeenCalledWith(
      expect.objectContaining({ offset: 200n, sortKey: IdenticalSortKey.SIZE, sortOrder: IdenticalSortOrder.ASC }),
      expect.anything(),
    ),
  );
  expect(within(pane).getByRole("listitem", { name: "copy-505.txt" })).toBeInTheDocument();
  expect(pane.querySelectorAll("[data-chonky-sparse-placeholder]")).toHaveLength(0);
  finish();
  await within(pane).findByRole("listitem", { name: "copy-161.txt" });
  expect(pane.querySelectorAll("[data-chonky-sparse-placeholder]")).toHaveLength(0);
  await userEvent.click(screen.getByRole("button", { name: "Jump to row 740" }));
  await within(pane).findByRole("listitem", { name: "copy-494.txt" });
  expect(api.rows).toHaveBeenLastCalledWith(expect.objectContaining({ offset: 700n, sortOrder: IdenticalSortOrder.ASC }), expect.anything());
  expect(api.find).toHaveBeenCalledTimes(1);
});

it("reports delayed Find and page reads once, preserving the sparse viewport through errors and retry", async () => {
  const group = IdenticalGroup.create({ id: "group", name: "Related", memberCount: 999n });
  const rows = (offset: bigint) =>
    Array.from({ length: 100 }, (_, index) => {
      const position = offset + BigInt(index);
      const header = { position, group, groupHeaderPosition: 0n, displayMemberCount: 999n };
      return position === 0n
        ? header
        : {
            ...header,
            member: IdenticalMember.create({
              entry: {
                reference: { target: { oneofKind: "fileId", fileId: position } },
                associatedFileId: position,
                name: `file-${position}.txt`,
                path: `file-${position}.txt`,
                kind: EntryKind.FILE,
              },
            }),
          };
    });
  let finishFind!: (value: unknown) => void;
  const requests = new Map<bigint, { resolve: (value: unknown) => void; reject: (error: Error) => void; signal?: AbortSignal }>();
  api.find.mockReturnValue({
    response: new Promise((resolve) => {
      finishFind = resolve;
    }),
  });
  api.rows.mockImplementation(({ offset }: { offset: bigint }, options: { abort?: AbortSignal }) => ({
    response: new Promise((resolve, reject) => {
      requests.set(offset, { resolve, reject, signal: options.abort });
    }),
  }));
  api.close.mockReturnValue({ response: Promise.resolve({}) });
  render(
    <MemoryRouter>
      <IdenticalFiles />
    </MemoryRouter>,
  );
  await userEvent.click(screen.getByRole("button", { name: "Find" }));
  const pane = screen.getByRole("region", { name: "Identical file groups" });
  expect(within(pane).getAllByRole("progressbar")).toHaveLength(1);
  expect(within(pane).queryByText("No matching groups")).not.toBeInTheDocument();
  await act(async () => finishFind({ resultId: "result", allRowCount: 1000n, visibleRowCount: 1000n }));
  await waitFor(() => expect(requests.has(0n)).toBe(true));
  expect(screen.getByTestId("virtual-viewport")).toHaveAttribute("data-total-count", "1000");
  expect(within(pane).getAllByRole("progressbar")).toHaveLength(1);
  expect(within(pane).getByRole("progressbar")).toHaveAccessibleName("Loading files");
  expect(pane.querySelectorAll("[data-chonky-sparse-placeholder]")).toHaveLength(20);
  await act(async () => requests.get(0n)!.resolve({ rows: rows(0n), totalRowCount: 1000n }));
  await userEvent.click(await within(pane).findByRole("listitem", { name: "file-1.txt" }));
  expect(within(pane).queryByRole("progressbar")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Jump to row 240" }));
  await waitFor(() => expect(requests.has(200n)).toBe(true));
  expect(within(pane).getAllByRole("progressbar")).toHaveLength(1);
  const obsolete = requests.get(200n)!;
  await userEvent.click(screen.getByRole("button", { name: "Jump to row 740" }));
  await waitFor(() => expect(requests.has(700n)).toBe(true));
  expect(obsolete.signal?.aborted).toBe(true);
  await act(async () => obsolete.resolve({ rows: rows(200n), totalRowCount: 1000n }));
  expect(within(pane).getByRole("progressbar")).toBeInTheDocument();
  await act(async () => requests.get(700n)!.reject(new Error("Page unavailable")));
  expect(await screen.findByText(/Page unavailable/)).toBeInTheDocument();
  expect(within(pane).queryByRole("progressbar")).not.toBeInTheDocument();
  expect(screen.getByTestId("virtual-viewport")).toHaveAttribute("data-total-count", "1000");
  expect(screen.getByLabelText("Inspector")).toHaveTextContent("file-1.txt");
  await userEvent.click(screen.getByRole("button", { name: "Retry rows" }));
  expect(within(pane).getAllByRole("progressbar")).toHaveLength(1);
  await act(async () => requests.get(700n)!.resolve({ rows: rows(700n), totalRowCount: 1000n }));
  await within(pane).findByRole("listitem", { name: "file-740.txt" });
  expect(within(pane).queryByRole("progressbar")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Jump to row 0" }));
  expect(within(pane).getByRole("listitem", { name: "file-1.txt" })).toBeInTheDocument();
  expect(within(pane).getByText(/1 selected/)).toBeInTheDocument();
  expect(screen.getByLabelText("Inspector")).toHaveTextContent("file-1.txt");
  expect(api.rows.mock.calls.filter(([request]) => request.offset === 0n)).toHaveLength(1);
});

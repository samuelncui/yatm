import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef, useState, type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ChonkyActions, FileBrowser, FileToolbar, type FileArray, type FileBrowserHandle } from "@samuelncui/chonky";
import { ContentObservation, DuplicateGroup, DuplicateMember, FileScope, ListDuplicateGroupsReply, ListDuplicateMembersReply } from "@/entity";
import { DuplicateGroups, duplicateFiles } from "./duplicate-groups";
import { selectionForFile } from "./location-files";
import { chonkyI18n } from "@/tools";

const { listDuplicateGroups, listDuplicateMembers } = vi.hoisted(() => ({ listDuplicateGroups: vi.fn(), listDuplicateMembers: vi.fn() }));
vi.mock("@/api", async (original) => ({
  ...(await original<typeof import("@/api")>()),
  fileCatalogCli: { listDuplicateGroups, listDuplicateMembers },
}));

// Exercise the installed shared grouping, rows and selection; jsdom has no virtual viewport.
vi.mock("react-virtuoso", () => {
  const Items = ({ totalCount, itemContent }: { totalCount: number; itemContent: (index: number) => ReactNode }) => (
    <div>
      {Array.from({ length: totalCount }, (_, index) => (
        <div key={index}>{itemContent(index)}</div>
      ))}
    </div>
  );
  return { Virtuoso: Items, VirtuosoGrid: Items };
});

const groups = [2n, 3n, 4n].map((count, index) =>
  DuplicateGroup.create({
    signature: new Uint8Array([0, 255, index]),
    name: `Group ${index + 1}`,
    originalCount: count,
    matchingCount: index ? count : 1n,
    locationCount: index ? 1n : 2n,
    size: 12n,
  }),
);
const member = (id: bigint, group = 0, matchesFilter = true) =>
  DuplicateMember.create({
    file: {
      id,
      parentId: 1n,
      name: `photo-${id}.png`,
      note: "Independent note",
      tags: ["review"],
      size: 12n,
      contentSummary: { hasOriginal: true, signatureKnown: true, currentObservationValid: true, originalAvailability: 1 },
    },
    original: { fileId: id, locationId: 9n, path: `camera/photo-${id}.png`, signature: groups[group].signature },
    libraryPath: `/Photos/photo-${id}.png`,
    locationName: "Pictures",
    matchesFilter,
    observation: ContentObservation.CONFIRMED,
  });
const call = <T,>(response: T) => ({ response: Promise.resolve(response) });
const browser = createRef<FileBrowserHandle>();

function Review({
  query = "has:duplicates",
  refresh = { sequence: 0, background: false },
}: {
  query?: string;
  refresh?: { sequence: number; background: boolean };
}) {
  const [files, setFiles] = useState<FileArray>([]);
  return (
    <FileBrowser ref={browser} files={files} i18n={chonkyI18n} disableDragAndDrop defaultFileViewActionId={ChonkyActions.EnableListView.id}>
      <FileToolbar />
      <DuplicateGroups query={query} refresh={refresh} onFiles={setFiles} />
    </FileBrowser>
  );
}

beforeEach(() => {
  listDuplicateGroups.mockReset();
  listDuplicateMembers.mockReset();
  listDuplicateGroups.mockReturnValue(call(ListDuplicateGroupsReply.create({ groups, indexRevision: "index-1" })));
  listDuplicateMembers.mockImplementation(({ signature }: { signature: Uint8Array }) => {
    const group = signature[2];
    return call(
      ListDuplicateMembersReply.create({
        group: groups[group],
        indexRevision: "index-1",
        members: Array.from({ length: Number(groups[group].originalCount) }, (_, index) =>
          member(BigInt(group * 10 + index + 1), group, index === 0 || group > 0),
        ),
      }),
    );
  });
});

describe("Duplicate content groups", () => {
  it("does not present stale or unavailable observations as confirmed duplicate contents", () => {
    for (const observation of [ContentObservation.CHANGED, ContentObservation.UNAVAILABLE, ContentObservation.UNCHECKED]) {
      const value = member(1n);
      value.observation = observation;
      const row = duplicateFiles([value])[0];
      expect(row.status?.color).toBe("#87909e");
      expect(row.details).toHaveLength(3);
      expect(row.analysisPath).toBe(value.original?.path);
    }
  });
  it("keeps unbacked duplicate members selectable when the Library hides unbacked Files", () => {
    const file = duplicateFiles([member(1n)])[0];
    expect(selectionForFile(file, FileScope.SAVED)).toMatchObject({ scope: FileScope.ALL, target: { oneofKind: "library", library: { fileId: 1n } } });
  });
  it("shows 2/3/4-file groups and retains shared selection, paths and status without selecting a representative", async () => {
    render(<Review />);
    expect(await screen.findByText("3 groups")).toBeInTheDocument();
    for (const count of [2, 3, 4]) expect(screen.getByText(`${count} files`)).toBeInTheDocument();
    expect(listDuplicateMembers).not.toHaveBeenCalled();
    expect(browser.current?.getFileSelection()).toEqual(new Set());
    await userEvent.click(screen.getByRole("button", { name: /Group 1/ }));
    expect(await screen.findByText("Library/Photos/photo-1.png")).toBeInTheDocument();
    expect(screen.getByRole("list").parentElement).toHaveStyle({ display: "flex", flexDirection: "column" });
    expect(screen.getByText("Pictures / camera/photo-2.png · Outside filter")).toBeInTheDocument();
    expect(screen.getAllByRole("img", { name: "Local file present · No usable backup" })).toHaveLength(2);
    await userEvent.click(screen.getByText("Library/Photos/photo-1.png"));
    await waitFor(() => expect(browser.current?.getFileSelection()).toEqual(new Set(["1"])));
    expect(screen.getByRole("button", { name: /Group 1/ })).toHaveAttribute("aria-expanded", "true");
    await userEvent.click(screen.getByRole("button", { name: /Group 2/ }));
    expect(await screen.findByText("Library/Photos/photo-11.png")).toBeInTheDocument();
    expect(screen.queryByText("Library/Photos/photo-1.png")).not.toBeInTheDocument();
    await waitFor(() => expect(browser.current?.getFileSelection()).toEqual(new Set()));
    expect(screen.getByRole("button", { name: /Group 1/ })).toHaveAttribute("aria-expanded", "false");
  });

  it("pages groups and members independently without turning a partial page into a group count", async () => {
    listDuplicateGroups
      .mockReturnValueOnce(call(ListDuplicateGroupsReply.create({ groups: [groups[2]], nextCursor: "group-next", indexRevision: "index-1" })))
      .mockReturnValueOnce(call(ListDuplicateGroupsReply.create({ groups: [groups[0]], indexRevision: "index-1" })));
    listDuplicateMembers
      .mockReturnValueOnce(
        call(
          ListDuplicateMembersReply.create({
            group: groups[2],
            members: [member(21n, 2), member(22n, 2)],
            nextCursor: "member-next",
            indexRevision: "index-1",
          }),
        ),
      )
      .mockReturnValueOnce(call(ListDuplicateMembersReply.create({ group: groups[2], members: [member(23n, 2), member(24n, 2)], indexRevision: "index-1" })));
    render(<Review query="tag:review" />);
    await userEvent.click(await screen.findByRole("button", { name: /Group 3/ }));
    expect(await screen.findByText("Library/Photos/photo-21.png")).toBeInTheDocument();
    expect(screen.getByText("4 files")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Load more files" }));
    expect(await screen.findByText("Library/Photos/photo-24.png")).toBeInTheDocument();
    expect(listDuplicateMembers).toHaveBeenLastCalledWith({ signature: groups[2].signature, query: "tag:review", cursor: "member-next", limit: 50 });
    await userEvent.click(screen.getByRole("button", { name: "Load more groups" }));
    expect(await screen.findByText("2 groups")).toBeInTheDocument();
    expect(listDuplicateGroups).toHaveBeenLastCalledWith({ query: "tag:review", cursor: "group-next", limit: 20 });
    expect(screen.getByText("Library/Photos/photo-21.png")).toBeInTheDocument();
  });

  it("ignores a late member response after another group is opened", async () => {
    let resolve!: (value: ListDuplicateMembersReply) => void;
    listDuplicateMembers.mockReturnValueOnce({
      response: new Promise((done) => {
        resolve = done;
      }),
    });
    render(<Review />);
    await userEvent.click(await screen.findByRole("button", { name: /Group 1/ }));
    await userEvent.click(screen.getByRole("button", { name: /Group 2/ }));
    expect(await screen.findByText("Library/Photos/photo-11.png")).toBeInTheDocument();
    await act(async () => resolve(ListDuplicateMembersReply.create({ group: groups[0], members: [member(1n)], indexRevision: "index-1" })));
    expect(screen.queryByText("Library/Photos/photo-1.png")).not.toBeInTheDocument();
    expect(screen.getByText("Library/Photos/photo-11.png")).toBeInTheDocument();
  });

  it("does not interrupt a pending first page or load-more request with background polling", async () => {
    let finish!: (value: ListDuplicateGroupsReply) => void;
    listDuplicateGroups.mockImplementationOnce(() => ({
      response: new Promise<ListDuplicateGroupsReply>((resolve) => {
        finish = resolve;
      }),
    }));
    const view = render(<Review />);
    view.rerender(<Review refresh={{ sequence: 1, background: true }} />);
    expect(listDuplicateGroups).toHaveBeenCalledTimes(1);
    await act(async () => finish(ListDuplicateGroupsReply.create({ groups: [groups[0]], nextCursor: "next", indexRevision: "index-1" })));
    expect(await screen.findByText("1 group loaded")).toBeInTheDocument();
    listDuplicateGroups.mockImplementationOnce(() => ({
      response: new Promise<ListDuplicateGroupsReply>((resolve) => {
        finish = resolve;
      }),
    }));
    await userEvent.click(screen.getByRole("button", { name: "Load more groups" }));
    view.rerender(<Review refresh={{ sequence: 2, background: true }} />);
    expect(listDuplicateGroups).toHaveBeenCalledTimes(2);
    await act(async () => finish(ListDuplicateGroupsReply.create({ groups: [groups[1]], indexRevision: "index-1" })));
    expect(await screen.findByText("2 groups")).toBeInTheDocument();
    expect(screen.queryByText(/Results changed/)).not.toBeInTheDocument();
  });

  it("offers refresh and resets expansions after an observed index change", async () => {
    const view = render(<Review />);
    await userEvent.click(await screen.findByRole("button", { name: /Group 1/ }));
    expect(await screen.findByText("Library/Photos/photo-1.png")).toBeInTheDocument();
    listDuplicateGroups.mockReturnValue(call(ListDuplicateGroupsReply.create({ groups: [groups[2]], indexRevision: "index-2" })));
    view.rerender(<Review refresh={{ sequence: 1, background: true }} />);
    expect(await screen.findByText(/Results changed/)).toBeInTheDocument();
    expect(screen.queryByText("Library/Photos/photo-1.png")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Refresh results" }));
    expect(await screen.findByText("1 group")).toBeInTheDocument();
    expect(screen.queryByText(/Results changed/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Group 3/ })).toHaveAttribute("aria-expanded", "false");
  });

  it("retries group and member errors independently and rejects obsolete singletons", async () => {
    listDuplicateGroups.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Group query failed")) }));
    render(<Review />);
    expect(await screen.findByText("Group query failed")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Retry groups" }));
    listDuplicateMembers.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Member query failed")) }));
    await userEvent.click(await screen.findByRole("button", { name: /Group 1/ }));
    expect(await screen.findByText("Member query failed")).toBeInTheDocument();
    listDuplicateMembers.mockReturnValueOnce(call(ListDuplicateMembersReply.create({ group: { ...groups[0], originalCount: 1n }, indexRevision: "index-1" })));
    await userEvent.click(screen.getByRole("button", { name: "Retry files" }));
    expect(await screen.findByText(/Results changed/)).toBeInTheDocument();
    expect(screen.queryByText("Library/Photos/photo-1.png")).not.toBeInTheDocument();
  });
});

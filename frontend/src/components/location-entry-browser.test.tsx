import { act, screen, waitFor, within } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { EntryKind, FilesDetail, FilesEntry, Location, LocationEntryRef } from "@/entity";
import { livePage, streamListing } from "@/test/files-fixture";

const { listEntries } = vi.hoisted(() => ({ listEntries: vi.fn() }));
const { fakePage } = vi.hoisted(() => ({ fakePage: vi.fn() }));
vi.mock("@/components/files-browser", async (original) => {
  const actual = await original<typeof import("@/components/files-browser")>();
  return {
    ...actual,
    filesPage: (...args: Parameters<typeof actual.filesPage>) => (fakePage.getMockImplementation() ? fakePage(...args) : actual.filesPage(...args)),
  };
});
vi.mock("@/api", async (original) => ({
  ...(await original<typeof import("@/api")>()),
  locationCli: { listEntries },
  filesCli: {
    list: ({ directory }: any) => streamListing(listEntries().response.then((reply: any) => livePage(reply, directory.target.location.locationId))),
    get: ({ reference }: any) => ({
      response: Promise.resolve({
        detail: FilesDetail.create({ entry: FilesEntry.create({ reference, name: "report.txt", kind: EntryKind.FILE, sizeBytes: 42n }) }),
      }),
    }),
  },
}));
import { LocationEntryBrowser } from "./location-entry-browser";

// Keep Chonky's real click handling; jsdom has no virtual viewport geometry.
vi.mock("react-virtuoso", () => {
  const Viewport = ({ totalCount, itemContent, view }: { totalCount: number; itemContent: (index: number) => ReactNode; view: "list" | "grid" }) => (
    <div data-testid="location-viewport" data-view={view}>
      {Array.from({ length: totalCount }, (_, index) => (
        <div key={index}>{itemContent(index)}</div>
      ))}
    </div>
  );
  return {
    Virtuoso: (props: Omit<React.ComponentProps<typeof Viewport>, "view">) => <Viewport {...props} view="list" />,
    VirtuosoGrid: (props: Omit<React.ComponentProps<typeof Viewport>, "view">) => <Viewport {...props} view="grid" />,
  };
});

afterEach(() => {
  vi.restoreAllMocks();
  fakePage.mockReset();
});

it.each([false, true])("uses double-click for details, or explicit picker selection (picker %s)", async (picker) => {
  const open = vi.spyOn(window, "open").mockImplementation(() => null);
  const choose = vi.fn();
  const reference = LocationEntryRef.create({ locationId: 4n, path: "report.txt", facts: { mode: 420, sizeBytes: 42n } });
  listEntries.mockReturnValue({ response: Promise.resolve({ entries: [{ path: reference.path, reference }], hasMore: false }) });
  render(<LocationEntryBrowser source={Location.create({ id: 4n, name: "Documents" })} onChoose={picker ? choose : undefined} />, { wrapper: MemoryRouter });
  await userEvent.dblClick((await screen.findAllByTitle("report.txt"))[0]);
  if (picker) {
    await waitFor(() => expect(choose).toHaveBeenCalledWith(reference));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  } else {
    const dialog = await screen.findByRole("dialog", { name: "report.txt" });
    expect(dialog).toHaveTextContent("report.txt");
    await within(dialog).findByText("42 B");
    expect(choose).not.toHaveBeenCalled();
  }
  expect(open).not.toHaveBeenCalled();
});

it("hides Sort and folders-first while the Location read is pending or incomplete, then restores them", async () => {
  let finishFirst!: (value: { files: { id: string; name: string; size: number }[]; nextCursor: string }) => void;
  const first = new Promise<{ files: { id: string; name: string; size: number }[]; nextCursor: string }>((resolve) => {
    finishFirst = resolve;
  });
  fakePage.mockImplementation((_directory, _scope, cursor = "") =>
    cursor ? Promise.resolve({ files: [{ id: "2", name: "alpha.txt", size: 10 }], nextCursor: "" }) : first,
  );
  render(<LocationEntryBrowser source={Location.create({ id: 4n, name: "Documents" })} />, { wrapper: MemoryRouter });
  const checkOptions = async (sortVisible: boolean) => {
    await userEvent.click(screen.getByRole("button", { name: "Options" }));
    if (sortVisible) {
      expect(screen.getByRole("menuitem", { name: "Sort by size" })).toBeInTheDocument();
      expect(screen.getByRole("menuitem", { name: "Show folders first" })).toBeInTheDocument();
    } else {
      expect(screen.queryByRole("menuitem", { name: /Sort by/ })).not.toBeInTheDocument();
      expect(screen.queryByRole("menuitem", { name: "Show folders first" })).not.toBeInTheDocument();
    }
    expect(screen.queryByRole("menuitem", { name: /Switch to/ })).not.toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Actions" }));
    if (sortVisible) expect(screen.getByRole("menuitem", { name: "Select all files" })).toBeInTheDocument();
    else expect(screen.queryByRole("menuitem", { name: "Select all files" })).not.toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
  };
  await checkOptions(false);
  await act(async () => finishFirst({ files: [{ id: "1", name: "zulu.txt", size: 20 }], nextCursor: "next" }));
  await waitFor(() => expect(screen.getByTestId("location-viewport")).toHaveTextContent("zulu.txt"));
  await checkOptions(false);
  await userEvent.click(screen.getByRole("button", { name: "Load More" }));
  await waitFor(() => expect(screen.getByTestId("location-viewport")).toHaveTextContent("alpha.txt"));
  await checkOptions(true);
  await userEvent.click(screen.getByRole("button", { name: "Options" }));
  await userEvent.click(screen.getByRole("menuitem", { name: "Sort by size" }));
  await waitFor(() =>
    expect(
      within(screen.getByTestId("location-viewport"))
        .getAllByRole("listitem")
        .map((row) => row.textContent?.match(/(?:alpha|zulu)\.txt/)?.[0]),
    ).toEqual(["zulu.txt", "alpha.txt"]),
  );
  expect(fakePage).toHaveBeenCalledTimes(2);
});

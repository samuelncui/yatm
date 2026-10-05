import type { ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { JobResultOrder, ScanEntry } from "@/entity";

const { listEntries } = vi.hoisted(() => ({ listEntries: vi.fn() }));
vi.mock("@/api", () => ({ scanJobCli: { listEntries }, archiveJobCli: {}, restoreJobCli: {} }));
vi.mock("react-virtuoso", () => ({
  Virtuoso: ({
    totalCount = 0,
    itemContent,
    rangeChanged,
  }: {
    totalCount?: number;
    itemContent: (index: number) => ReactNode;
    rangeChanged?: (range: { startIndex: number; endIndex: number }) => void;
  }) => (
    <div>
      {[0, 200, 449, 450].map((index) => (
        <button key={index} onClick={() => rangeChanged?.({ startIndex: index, endIndex: index })}>
          {`range ${index}`}
        </button>
      ))}
      <div>
        {Array.from({ length: totalCount }, (_, index) => (
          <div key={index}>{itemContent(index)}</div>
        ))}
      </div>
    </div>
  ),
}));
import { JobResultsDialog, type JobItemViews } from "./job-results-dialog";

const total = 500;
const views: JobItemViews = [
  {
    id: "files",
    label: "Files",
    emptyLabel: "No results yet.",
    listing: "scan-entries",
    cursorOf: (entry: ScanEntry) => String(entry.id),
    render: (entry: ScanEntry) => <span>{entry.path}</span>,
  },
];

/** Serves a frozen manifest of `total` rows for whichever anchor it receives. */
const serveManifest = (request: { limit: number; cursor: string; order: JobResultOrder; offset?: bigint; includeTotal: boolean }) => {
  const descending = request.order === JobResultOrder.DESCENDING;
  const anchor = request.cursor ? Number(request.cursor) : request.offset !== undefined ? Number(request.offset) : 0;
  const first = request.cursor || request.offset !== undefined ? anchor + 1 : 1;
  const ids: number[] = [];
  for (let step = 0; step < request.limit; step++) {
    const id = descending ? anchor - 1 - step : first + step;
    if (id < 1 || id > total) break;
    ids.push(id);
  }
  return {
    response: Promise.resolve({
      entries: ids.map((id) => ScanEntry.create({ id: BigInt(id), path: `file-${id}.txt` })),
      hasMore: ids.length === request.limit,
      totalEntryCount: request.includeTotal ? BigInt(total) : undefined,
    }),
  };
};

beforeEach(() => {
  vi.clearAllMocks();
  listEntries.mockImplementation(serveManifest);
});

describe("Job results list", () => {
  it("spans the whole result set and continues from the last loaded row", async () => {
    render(<JobResultsDialog jobId={7n} title="Scan results" views={views} />);
    await userEvent.click(screen.getByRole("button", { name: "Results" }));

    // The first load carries the size of the list; later windows must not repeat that query.
    expect(await screen.findByText("file-1.txt")).toBeInTheDocument();
    expect(listEntries).toHaveBeenNthCalledWith(1, {
      id: 7n,
      limit: 200,
      cursor: "",
      order: JobResultOrder.ASCENDING,
      includeTotal: true,
    });
    expect(screen.queryByText("file-201.txt")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "range 200" }));
    expect(await screen.findByText("file-201.txt")).toBeInTheDocument();
    expect(listEntries).toHaveBeenNthCalledWith(2, {
      id: 7n,
      limit: 200,
      cursor: "200",
      order: JobResultOrder.ASCENDING,
      includeTotal: false,
    });
  });

  it("anchors a distant position with an offset and then fills upward", async () => {
    render(<JobResultsDialog jobId={7n} title="Scan results" views={views} />);
    await userEvent.click(screen.getByRole("button", { name: "Results" }));
    await screen.findByText("file-1.txt");

    // Nothing loaded is adjacent to row 450, so only an offset can locate it.
    await userEvent.click(screen.getByRole("button", { name: "range 450" }));
    expect(await screen.findByText("file-451.txt")).toBeInTheDocument();
    expect(listEntries).toHaveBeenNthCalledWith(2, {
      id: 7n,
      limit: 200,
      cursor: "",
      order: JobResultOrder.ASCENDING,
      offset: 450n,
      includeTotal: false,
    });

    // The window above the anchor is empty and is filled backwards from its first row.
    await userEvent.click(screen.getByRole("button", { name: "range 449" }));
    expect(await screen.findByText("file-450.txt")).toBeInTheDocument();
    expect(listEntries).toHaveBeenNthCalledWith(3, {
      id: 7n,
      limit: 200,
      cursor: "451",
      order: JobResultOrder.DESCENDING,
      includeTotal: false,
    });
  });

  it("keeps loaded rows when a later window fails and retries without discarding them", async () => {
    render(<JobResultsDialog jobId={7n} title="Scan results" views={views} />);
    await userEvent.click(screen.getByRole("button", { name: "Results" }));
    await screen.findByText("file-1.txt");

    listEntries.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Results unavailable")) }));
    await userEvent.click(screen.getByRole("button", { name: "range 450" }));
    expect(await screen.findByText("Results unavailable")).toBeInTheDocument();
    expect(screen.getByText("file-1.txt")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("file-1.txt")).toBeInTheDocument();
    expect(screen.queryByText("Results unavailable")).not.toBeInTheDocument();
  });
});

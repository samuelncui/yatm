import type { ReactNode } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { JobLogDirection, type JobLogLine, type ListJobLogLinesResponse } from "@/entity/job";

type VirtuosoProps = {
  data: JobLogLine[];
  itemContent: (index: number, line: JobLogLine) => ReactNode;
  initialTopMostItemIndex?: number;
  firstItemIndex?: number;
  followOutput?: unknown;
  atBottomStateChange?: (atBottom: boolean) => void;
  startReached?: () => void;
  endReached?: () => void;
};
const { listLogLines, virtuoso } = vi.hoisted(() => ({ listLogLines: vi.fn(), virtuoso: { current: undefined as VirtuosoProps | undefined } }));
vi.mock("@/api", () => ({ jobCli: { listLogLines } }));
vi.mock("react-virtuoso", () => ({
  Virtuoso: (props: VirtuosoProps) => {
    virtuoso.current = props;
    return (
      <div data-testid="log-lines">
        {props.data.map((line, index) => (
          <div key={line.offset.toString()}>{props.itemContent(index, line)}</div>
        ))}
      </div>
    );
  },
}));
import { ViewLogDialog } from "./job-log";

const line = (offset: bigint, text: string, level = "info", complete = true): JobLogLine => ({
  offset,
  endOffset: offset + BigInt(text.length + (complete ? 1 : 0)),
  text,
  level,
  continuation: false,
  complete,
});
const page = (lines: JobLogLine[], beforeCursor: bigint, afterCursor: bigint, hasOlder = false, hasNewer = false): ListJobLogLinesResponse => ({
  lines,
  beforeCursor,
  afterCursor,
  hasOlder,
  hasNewer,
});
const result = (reply: ListJobLogLinesResponse) => ({ response: Promise.resolve(reply) });
const open = (active = false) => {
  render(<ViewLogDialog jobID={7n} active={active} />);
  fireEvent.click(screen.getByRole("button", { name: "View Log" }));
};
const filterErrors = () => {
  fireEvent.mouseDown(screen.getByRole("combobox", { name: "Level" }));
  fireEvent.click(screen.getByRole("option", { name: "error" }));
};

beforeEach(() => {
  vi.clearAllMocks();
  listLogLines.mockReturnValue(result(page([], 0n, 0n)));
});

afterEach(() => vi.useRealTimers());

it("opens at the log tail and pages older lines using the returned cursor", async () => {
  listLogLines
    .mockReturnValueOnce(result(page([line(5000000n, "time=now level=error msg=last failure", "error")], 5000000n, 5000042n, true)))
    .mockReturnValueOnce(result(page([line(4000000n, "time=then level=info msg=earlier")], 4000000n, 5000000n)));
  open();
  expect(await screen.findByText(/last failure/)).toBeInTheDocument();
  expect(listLogLines).toHaveBeenNthCalledWith(1, { id: 7n, direction: JobLogDirection.OLDER, cursor: undefined, level: "", query: "" });
  expect(virtuoso.current?.initialTopMostItemIndex).toBe((virtuoso.current?.firstItemIndex ?? 0) + 1 - 1);

  fireEvent.click(screen.getByRole("button", { name: "Load older" }));
  expect(await screen.findByText(/earlier/)).toBeInTheDocument();
  expect(listLogLines).toHaveBeenNthCalledWith(2, { id: 7n, direction: JobLogDirection.OLDER, cursor: 5000000n, level: "", query: "" });
  expect(screen.getByText(/last failure/)).toBeInTheDocument();
  expect(virtuoso.current?.data.map((row) => row.offset)).toEqual([4000000n, 5000000n]);
});

it("retries a failed initial read from the tail", async () => {
  const recovered = line(80n, "level=info msg=recovered");
  listLogLines
    .mockImplementationOnce(() => ({ response: Promise.reject(new Error("temporary failure")) }))
    .mockReturnValueOnce(result(page([recovered], 80n, recovered.endOffset)));
  open();

  expect(await screen.findByRole("alert")).toHaveTextContent("temporary failure");
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));

  expect(await screen.findByText(/recovered/)).toBeInTheDocument();
  expect(listLogLines).toHaveBeenNthCalledWith(2, { id: 7n, direction: JobLogDirection.OLDER, cursor: undefined, level: "", query: "" });
});

it("retries a failed newer read from its failed cursor", async () => {
  const first = line(80n, "level=info msg=first");
  const next = line(first.endOffset, "level=info msg=next");
  listLogLines
    .mockReturnValueOnce(result(page([first], 80n, first.endOffset, false, true)))
    .mockImplementationOnce(() => ({ response: Promise.reject(new Error("temporary failure")) }))
    .mockReturnValueOnce(result(page([next], first.endOffset, next.endOffset)));
  open();
  expect(await screen.findByText(/first/)).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Load newer" }));

  expect(await screen.findByRole("alert")).toHaveTextContent("temporary failure");
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));

  expect(await screen.findByText(/next/)).toBeInTheDocument();
  expect(listLogLines).toHaveBeenNthCalledWith(3, { id: 7n, direction: JobLogDirection.NEWER, cursor: first.endOffset, level: "", query: "" });
});

it("searches past an empty filtered page and highlights the retained raw line", async () => {
  listLogLines
    .mockReturnValueOnce(result(page([], 0n, 0n)))
    .mockReturnValueOnce(result(page([], 300n, 500n, true)))
    .mockReturnValueOnce(result(page([line(100n, "time=now level=error msg=needle", "error")], 0n, 300n)));
  open();
  await waitFor(() => expect(listLogLines).toHaveBeenCalledTimes(1));
  fireEvent.mouseDown(screen.getByRole("combobox", { name: "Level" }));
  fireEvent.click(screen.getByRole("option", { name: "error" }));
  expect(await screen.findByText(/needle/)).toBeInTheDocument();
  expect(listLogLines).toHaveBeenNthCalledWith(3, { id: 7n, direction: JobLogDirection.OLDER, cursor: 300n, level: "error", query: "" });
  expect(screen.getByText(/needle/).closest(".job-log-line")).toHaveClass("job-log-line--error");
  expect(screen.getByText("time=now")).toHaveClass("job-log-timestamp");
});

it("continues past an unverified long-line fragment during a filtered search", async () => {
  const fragment = { ...line(300n, "unrelated fragment", ""), continuation: true };
  const confirmed = line(100n, "level=error msg=needle", "error");
  listLogLines
    .mockReturnValueOnce(result(page([], 0n, 0n)))
    .mockReturnValueOnce(result(page([fragment], 300n, 500n, true)))
    .mockReturnValueOnce(result(page([confirmed], 0n, 300n)));
  open();
  await waitFor(() => expect(listLogLines).toHaveBeenCalledTimes(1));
  filterErrors();

  expect(await screen.findByText(/needle/)).toBeInTheDocument();
  expect(listLogLines).toHaveBeenNthCalledWith(3, { id: 7n, direction: JobLogDirection.OLDER, cursor: 300n, level: "error", query: "" });
  expect(screen.getByText(/Match unknown/).closest(".job-log-line")).toHaveAttribute("title", "Filter match unknown: continued line");
});

it("ignores an obsolete response after changing the filter", async () => {
  let resolveOld!: (reply: ListJobLogLinesResponse) => void;
  listLogLines
    .mockReturnValueOnce({
      response: new Promise<ListJobLogLinesResponse>((resolve) => {
        resolveOld = resolve;
      }),
    })
    .mockReturnValueOnce(result(page([line(9n, "level=error msg=current", "error")], 0n, 33n)));
  open();
  await waitFor(() => expect(listLogLines).toHaveBeenCalledTimes(1));
  fireEvent.mouseDown(screen.getByRole("combobox", { name: "Level" }));
  fireEvent.click(screen.getByRole("option", { name: "error" }));
  expect(await screen.findByText(/current/)).toBeInTheDocument();
  await act(async () => resolveOld(page([line(0n, "level=info msg=obsolete")], 0n, 24n)));
  expect(screen.queryByText(/obsolete/)).not.toBeInTheDocument();
});

it("stops before the first filtered reply and offers a resume from the beginning", async () => {
  let resolveOld!: (reply: ListJobLogLinesResponse) => void;
  const resumed = line(20n, "level=error msg=resumed", "error");
  listLogLines
    .mockReturnValueOnce(result(page([], 0n, 0n)))
    .mockReturnValueOnce({ response: new Promise<ListJobLogLinesResponse>((resolve) => (resolveOld = resolve)) })
    .mockReturnValueOnce(result(page([resumed], 0n, resumed.endOffset)));
  open();
  await waitFor(() => expect(listLogLines).toHaveBeenCalledTimes(1));
  filterErrors();
  await waitFor(() => expect(listLogLines).toHaveBeenCalledTimes(2));

  fireEvent.click(screen.getByRole("button", { name: "Stop search" }));
  expect(screen.getByRole("status")).toHaveTextContent("Search stopped.");
  expect(screen.getByRole("button", { name: "Resume search" })).toBeEnabled();
  fireEvent.click(screen.getByRole("button", { name: "Resume search" }));
  expect(await screen.findByText(/resumed/)).toBeInTheDocument();
  expect(listLogLines).toHaveBeenNthCalledWith(3, { id: 7n, direction: JobLogDirection.OLDER, cursor: undefined, level: "error", query: "" });

  await act(async () => resolveOld(page([line(0n, "level=info msg=obsolete")], 0n, 24n)));
  expect(screen.queryByText(/obsolete/)).not.toBeInTheDocument();
});

it("keeps a stopped active search paused until Resume", async () => {
  vi.useFakeTimers();
  let resolveOld!: (reply: ListJobLogLinesResponse) => void;
  const resumed = line(100n, "level=error msg=resumed", "error");
  listLogLines
    .mockReturnValueOnce(result(page([], 0n, 0n)))
    .mockReturnValueOnce(result(page([], 300n, 500n, true)))
    .mockReturnValueOnce({ response: new Promise<ListJobLogLinesResponse>((resolve) => (resolveOld = resolve)) })
    .mockReturnValueOnce(result(page([resumed], 0n, 300n)));
  open(true);
  await act(async () => {});
  expect(listLogLines).toHaveBeenCalledTimes(1);
  filterErrors();
  await act(async () => {});
  expect(listLogLines).toHaveBeenCalledTimes(3);

  fireEvent.click(screen.getByRole("button", { name: "Stop search" }));
  expect(screen.getByRole("status")).toHaveTextContent("Search stopped.");
  expect(screen.queryByText("No matching log lines.")).not.toBeInTheDocument();
  await act(async () => vi.advanceTimersByTimeAsync(2200));
  expect(listLogLines).toHaveBeenCalledTimes(3);

  fireEvent.click(screen.getByRole("button", { name: "Resume search" }));
  await act(async () => {});
  expect(screen.getByText(/resumed/)).toBeInTheDocument();
  expect(listLogLines).toHaveBeenNthCalledWith(4, { id: 7n, direction: JobLogDirection.OLDER, cursor: 300n, level: "error", query: "" });
  await act(async () => resolveOld(page([line(200n, "level=info msg=obsolete")], 200n, 300n)));
  expect(screen.queryByText(/obsolete/)).not.toBeInTheDocument();
});

it("keeps following an active Job after a scrolled log is filtered to no matches", async () => {
  vi.useFakeTimers();
  const old = line(0n, "level=info msg=old");
  const first = line(old.endOffset, "level=error msg=first match", "error");
  const second = line(first.endOffset, "level=error msg=second match", "error");
  listLogLines
    .mockReturnValueOnce(result(page([old], 0n, old.endOffset)))
    .mockReturnValueOnce(result(page([], old.endOffset, old.endOffset)))
    .mockReturnValueOnce(result(page([first], old.endOffset, first.endOffset)))
    .mockReturnValueOnce(result(page([second], first.endOffset, second.endOffset)));
  open(true);
  await act(async () => {});
  expect(screen.getByText(/msg=old/)).toBeInTheDocument();
  act(() => virtuoso.current?.atBottomStateChange?.(false));
  expect(virtuoso.current?.followOutput).toBe(false);

  filterErrors();
  await act(async () => {});
  expect(screen.getByText("No matching log lines.")).toBeInTheDocument();
  await act(async () => vi.advanceTimersByTimeAsync(2000));
  expect(screen.getByText(/first match/)).toBeInTheDocument();
  expect(virtuoso.current?.followOutput).toBe("auto");
  expect(listLogLines).toHaveBeenNthCalledWith(3, {
    id: 7n,
    direction: JobLogDirection.NEWER,
    cursor: old.endOffset,
    level: "error",
    query: "",
  });

  await act(async () => vi.advanceTimersByTimeAsync(2000));
  expect(screen.getByText(/second match/)).toBeInTheDocument();
  expect(listLogLines).toHaveBeenNthCalledWith(4, {
    id: 7n,
    direction: JobLogDirection.NEWER,
    cursor: first.endOffset,
    level: "error",
    query: "",
  });
});

it("polls an empty filtered page even when the old viewport reports atBottom false", async () => {
  vi.useFakeTimers();
  const old = line(0n, "level=info msg=old");
  const match = line(old.endOffset, "level=error msg=new match", "error");
  listLogLines
    .mockReturnValueOnce(result(page([old], 0n, old.endOffset)))
    .mockReturnValueOnce(result(page([], old.endOffset, old.endOffset)))
    .mockReturnValueOnce(result(page([match], old.endOffset, match.endOffset)));
  open(true);
  await act(async () => {});
  expect(screen.getByText(/msg=old/)).toBeInTheDocument();
  const reportAtBottom = virtuoso.current?.atBottomStateChange;

  filterErrors();
  await act(async () => {});
  expect(screen.getByText("No matching log lines.")).toBeInTheDocument();
  act(() => reportAtBottom?.(false));

  await act(async () => vi.advanceTimersByTimeAsync(2000));
  expect(screen.getByText(/new match/)).toBeInTheDocument();
  expect(listLogLines).toHaveBeenNthCalledWith(3, {
    id: 7n,
    direction: JobLogDirection.NEWER,
    cursor: old.endOffset,
    level: "error",
    query: "",
  });
});

it("loads newer lines and replaces a growing partial row", async () => {
  listLogLines
    .mockReturnValueOnce(result(page([line(0n, "level=info msg=half", "info", false)], 0n, 19n, false, true)))
    .mockReturnValueOnce(result(page([{ ...line(19n, " done"), continuation: true }], 19n, 25n)));
  open();
  expect(await screen.findByText(/half/)).toBeInTheDocument();
  expect(listLogLines).toHaveBeenCalledTimes(1);
  expect((await listLogLines.mock.results[0].value.response).hasNewer).toBe(true);
  fireEvent.click(await screen.findByRole("button", { name: "Load newer" }));
  expect(await screen.findByText(/half done/)).toBeInTheDocument();
  expect(virtuoso.current?.data).toHaveLength(1);
});

it("joins a level-matched continuation onto the filtered partial row", async () => {
  vi.useFakeTimers();
  const partial = line(0n, "level=error msg=half", "error", false);
  const continuation = { ...line(partial.endOffset, " done", "error"), continuation: true };
  listLogLines
    .mockReturnValueOnce(result(page([], 0n, 0n)))
    .mockReturnValueOnce(result(page([partial], 0n, partial.endOffset)))
    .mockReturnValueOnce(result(page([continuation], partial.endOffset, continuation.endOffset)));
  open(true);
  await act(async () => {});
  expect(listLogLines).toHaveBeenCalledTimes(1);
  filterErrors();
  await act(async () => {});
  expect(screen.getByText(/half/)).toBeInTheDocument();

  await act(async () => vi.advanceTimersByTimeAsync(2000));
  expect(screen.getByText(/half done/)).toBeInTheDocument();
  expect(listLogLines).toHaveBeenNthCalledWith(3, { id: 7n, direction: JobLogDirection.NEWER, cursor: partial.endOffset, level: "error", query: "" });
  expect(virtuoso.current?.data).toHaveLength(1);
  expect(virtuoso.current?.data[0]).toMatchObject({ offset: 0n, endOffset: continuation.endOffset, complete: true });
});

it("rechecks the raw partial tail before a text-filtered newer read", async () => {
  vi.useFakeTimers();
  const partial = line(0n, "level=info msg=need", "info", false);
  const complete = line(0n, "level=info msg=needle", "info");
  listLogLines
    .mockReturnValueOnce(result(page([], 0n, 0n)))
    .mockReturnValueOnce(result(page([], 0n, partial.endOffset)))
    .mockReturnValueOnce(result(page([partial], 0n, partial.endOffset)))
    .mockReturnValueOnce(result(page([complete], 0n, complete.endOffset)));
  open(true);
  await act(async () => {});
  expect(listLogLines).toHaveBeenCalledTimes(1);
  fireEvent.change(screen.getByRole("textbox", { name: "Search log" }), { target: { value: "needle" } });
  await act(async () => vi.advanceTimersByTimeAsync(300));
  expect(screen.getByText("No matching log lines.")).toBeInTheDocument();

  await act(async () => vi.advanceTimersByTimeAsync(2000));
  expect(screen.getByText(/needle/)).toBeInTheDocument();
  expect(listLogLines).toHaveBeenNthCalledWith(3, { id: 7n, direction: JobLogDirection.OLDER, cursor: partial.endOffset, level: "", query: "" });
  expect(listLogLines).toHaveBeenNthCalledWith(4, { id: 7n, direction: JobLogDirection.NEWER, cursor: 0n, level: "", query: "needle" });
});

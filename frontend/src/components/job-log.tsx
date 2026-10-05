import { Feedback } from "@/components/feedback";
import { Fragment, useCallback, useEffect, useRef, useState } from "react";

import Button from "@mui/material/Button";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogTitle from "@mui/material/DialogTitle";
import MenuItem from "@mui/material/MenuItem";
import TextField from "@mui/material/TextField";
import { Virtuoso } from "react-virtuoso";

import { jobCli } from "@/api";
import { JobLogDirection, type JobLogLine, type ListJobLogLinesResponse } from "@/entity/job";

const maxLoadedLines = 2000;
const maxLoadedCharacters = 4 * 1024 * 1024;
const firstItemAnchor = 1000000;
const pollInterval = 2000;

type LogPage = {
  lines: JobLogLine[];
  beforeCursor: bigint;
  afterCursor: bigint;
  hasOlder: boolean;
  hasNewer: boolean;
  firstItemIndex: number;
  loaded: boolean;
};

type LogRead = { direction: JobLogDirection; cursor?: bigint; initial: boolean };

const emptyPage = (): LogPage => ({
  lines: [],
  beforeCursor: 0n,
  afterCursor: 0n,
  hasOlder: false,
  hasNewer: false,
  firstItemIndex: firstItemAnchor,
  loaded: false,
});

const mergePage = (current: LogPage, reply: ListJobLogLinesResponse, direction: JobLogDirection, initial: boolean): LogPage => {
  if (initial) return { ...reply, lines: reply.lines, firstItemIndex: firstItemAnchor, loaded: true };

  const byOffset = new Map(current.lines.map((line) => [line.offset, line]));
  let incoming = reply.lines;
  const last = current.lines.at(-1);
  if (
    direction === JobLogDirection.NEWER &&
    last &&
    !last.complete &&
    incoming[0]?.continuation &&
    incoming[0].offset === last.endOffset &&
    last.text.length + incoming[0].text.length <= 1 << 20
  ) {
    byOffset.set(last.offset, { ...last, text: last.text + incoming[0].text, endOffset: incoming[0].endOffset, complete: incoming[0].complete });
    incoming = incoming.slice(1);
  }
  for (const line of incoming) byOffset.set(line.offset, line);
  const lines = [...byOffset.values()].sort((a, b) => (a.offset < b.offset ? -1 : a.offset > b.offset ? 1 : 0));
  const prepended = direction === JobLogDirection.OLDER && current.lines.length > 0 ? lines.filter((line) => line.offset < current.lines[0].offset).length : 0;
  let firstItemIndex = current.firstItemIndex - prepended;
  let beforeCursor = direction === JobLogDirection.OLDER ? reply.beforeCursor : current.beforeCursor;
  let afterCursor = direction === JobLogDirection.NEWER ? reply.afterCursor : current.afterCursor;
  let hasOlder = direction === JobLogDirection.OLDER ? reply.hasOlder : current.hasOlder;
  let hasNewer = direction === JobLogDirection.NEWER ? reply.hasNewer : current.hasNewer;
  let characters = lines.reduce((total, line) => total + line.text.length, 0);
  if (direction === JobLogDirection.OLDER) {
    while (lines.length > 1 && (lines.length > maxLoadedLines || characters > maxLoadedCharacters)) {
      characters -= lines[lines.length - 1].text.length;
      lines.pop();
      afterCursor = lines[lines.length - 1].endOffset;
      hasNewer = true;
    }
  } else {
    while (lines.length > 1 && (lines.length > maxLoadedLines || characters > maxLoadedCharacters)) {
      characters -= lines[0].text.length;
      lines.shift();
      firstItemIndex++;
      beforeCursor = lines[0].offset;
      hasOlder = true;
    }
  }
  return { lines, beforeCursor, afterCursor, hasOlder, hasNewer, firstItemIndex, loaded: true };
};

export const ViewLogDialog = ({ jobID, active }: { jobID: bigint; active: boolean }) => {
  const [open, setOpen] = useState(false);
  return (
    <Fragment>
      <Button size="small" onClick={() => setOpen(true)}>
        View Log
      </Button>
      {open && (
        <Dialog
          open
          onClose={() => setOpen(false)}
          maxWidth="lg"
          fullWidth
          scroll="paper"
          className="job-view-dialog"
          slotProps={{ paper: { sx: { height: "calc(100% - 64px)" } } }}
        >
          <DialogTitle>View Log</DialogTitle>
          <DialogContent dividers>
            <LogConsole jobId={jobID} active={active} />
          </DialogContent>
          <DialogActions>
            <Button onClick={() => setOpen(false)}>Close</Button>
          </DialogActions>
        </Dialog>
      )}
    </Fragment>
  );
};

const LogConsole = ({ jobId, active }: { jobId: bigint; active: boolean }) => {
  const [page, setPage] = useState<LogPage>(emptyPage);
  const [level, setLevel] = useState("");
  const [queryInput, setQueryInput] = useState("");
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [failedRead, setFailedRead] = useState<LogRead | null>(null);
  const [stoppedRead, setStoppedRead] = useState<LogRead | null>(null);
  const [atBottom, setAtBottom] = useState(true);
  const generation = useRef(0);
  const pending = useRef(false);
  const inFlight = useRef<LogRead | null>(null);

  useEffect(() => {
    const timer = setTimeout(() => setQuery(queryInput), 300);
    return () => clearTimeout(timer);
  }, [queryInput]);

  const load = useCallback(
    async (direction: JobLogDirection, cursor?: bigint, initial = false) => {
      if (pending.current) return;
      pending.current = true;
      setStoppedRead(null);
      setLoading(true);
      setError("");
      setFailedRead(null);
      const currentGeneration = generation.current;
      let nextCursor = cursor;
      try {
        // A text match may appear only after an unfinished line grows. Re-read that one line
        // from its start so the server evaluates the complete text, without scanning old pages.
        if (direction === JobLogDirection.NEWER && query && nextCursor && nextCursor > 0n) {
          inFlight.current = { direction, cursor: nextCursor, initial };
          const tail = await jobCli.listLogLines({ id: jobId, direction: JobLogDirection.OLDER, cursor: nextCursor, level: "", query: "" }).response;
          if (currentGeneration !== generation.current) return;
          const last = tail.lines.at(-1);
          if (last && !last.complete && last.endOffset === nextCursor) nextCursor = last.offset;
        }
        while (true) {
          inFlight.current = { direction, cursor: nextCursor, initial };
          const reply = await jobCli.listLogLines({ id: jobId, direction, cursor: nextCursor, level, query }).response;
          if (currentGeneration !== generation.current) return;
          const firstPage = initial;
          setPage((current) => mergePage(current, reply, direction, firstPage));
          initial = false;
          const more = direction === JobLogDirection.OLDER ? reply.hasOlder : reply.hasNewer;
          const advanced = direction === JobLogDirection.OLDER ? reply.beforeCursor !== nextCursor : reply.afterCursor !== nextCursor;
          const confirmed = reply.lines.some((line) => !line.continuation);
          if (confirmed || !more || !advanced || (!level && !query)) break;
          nextCursor = direction === JobLogDirection.OLDER ? reply.beforeCursor : reply.afterCursor;
        }
      } catch (cause) {
        if (currentGeneration === generation.current) {
          setError(cause instanceof Error ? cause.message : "Could not read Job log");
          setFailedRead(inFlight.current);
        }
      } finally {
        if (currentGeneration === generation.current) {
          pending.current = false;
          inFlight.current = null;
          setLoading(false);
        }
      }
    },
    [jobId, level, query],
  );

  useEffect(() => {
    generation.current++;
    pending.current = false;
    setPage(emptyPage());
    setAtBottom(true);
    void load(JobLogDirection.OLDER, undefined, true);
    const requestGeneration = generation;
    return () => {
      requestGeneration.current++;
    };
  }, [load]);

  useEffect(() => {
    if (!active || !page.loaded || (!atBottom && page.lines.length > 0) || stoppedRead) return;
    const timer = setInterval(() => void load(JobLogDirection.NEWER, page.afterCursor), pollInterval);
    return () => clearInterval(timer);
  }, [active, atBottom, load, page.afterCursor, page.lines.length, page.loaded, stoppedRead]);

  const older = () => {
    if (page.loaded && page.hasOlder && !stoppedRead) void load(JobLogDirection.OLDER, page.beforeCursor);
  };
  const newer = () => {
    if (page.loaded && page.hasNewer && !stoppedRead) void load(JobLogDirection.NEWER, page.afterCursor);
  };
  const stopSearch = () => {
    generation.current++;
    pending.current = false;
    setStoppedRead(inFlight.current);
    setLoading(false);
  };

  return (
    <div className="job-log-console">
      <div className="job-log-controls">
        <TextField select size="small" label="Level" className="job-log-level" value={level} onChange={(event) => setLevel(event.target.value)}>
          <MenuItem value="">All levels</MenuItem>
          {(["error", "warning", "info", "debug", "trace", "fatal", "panic"] as const).map((value) => (
            <MenuItem key={value} value={value}>
              {value}
            </MenuItem>
          ))}
        </TextField>
        <TextField size="small" label="Search log" className="job-log-query" value={queryInput} onChange={(event) => setQueryInput(event.target.value)} />
        {loading && <span role="status">Searching log…</span>}
        {loading && (level || query) && (
          <Button size="small" onClick={stopSearch}>
            Stop search
          </Button>
        )}
        {stoppedRead && (
          <span role="status">
            Search stopped.{" "}
            <Button size="small" onClick={() => void load(stoppedRead.direction, stoppedRead.cursor, stoppedRead.initial)}>
              Resume search
            </Button>
          </span>
        )}
      </div>
      {error && failedRead && (
        <Feedback
          action={
            <Button size="small" onClick={() => void load(failedRead.direction, failedRead.cursor, failedRead.initial)}>
              Retry
            </Button>
          }
        >
          {error}
        </Feedback>
      )}
      {page.loaded && page.hasOlder && (
        <Button size="small" onClick={older} disabled={loading || !!stoppedRead}>
          Load older
        </Button>
      )}
      {page.lines.length > 0 ? (
        <Virtuoso
          style={{ height: "60vh" }}
          className="job-log-lines"
          data={page.lines}
          firstItemIndex={page.firstItemIndex}
          initialTopMostItemIndex={page.firstItemIndex + page.lines.length - 1}
          followOutput={atBottom ? "auto" : false}
          atBottomStateChange={setAtBottom}
          startReached={older}
          endReached={newer}
          itemContent={(_index, line) => <LogLine line={line} filtered={!!(level || query)} />}
        />
      ) : page.loaded && !loading && !error && !stoppedRead ? (
        <p>No matching log lines.</p>
      ) : null}
      {page.loaded && page.hasNewer && (
        <Button size="small" onClick={newer} disabled={loading || !!stoppedRead}>
          Load newer
        </Button>
      )}
    </div>
  );
};

const LogLine = ({ line, filtered }: { line: JobLogLine; filtered: boolean }) => {
  const timestamp = /^(time=(?:"[^"]+"|\S+)|\d{4}-\d\d-\d\d[T ][^ ]+)/.exec(line.text)?.[0] ?? "";
  const severity = line.level === "error" || line.level === "fatal" || line.level === "panic" ? "error" : line.level;
  return (
    <div
      className={`job-log-line job-log-line--${severity || "plain"}`}
      title={line.continuation ? (filtered ? "Filter match unknown: continued line" : "Continued line") : undefined}
    >
      {line.continuation && (
        <span className="job-log-continuation" aria-hidden="true">
          ↳{" "}
        </span>
      )}
      {line.continuation && filtered && <span className="job-log-continuation">Match unknown · </span>}
      {timestamp && <span className="job-log-timestamp">{timestamp}</span>}
      {line.text.slice(timestamp.length)}
    </div>
  );
};

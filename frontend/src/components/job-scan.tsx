import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import Button from "@mui/material/Button";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogTitle from "@mui/material/DialogTitle";
import List from "@mui/material/List";
import ListItemText from "@mui/material/ListItemText";
import Alert from "@mui/material/Alert";
import Stack from "@mui/material/Stack";
import CircularProgress from "@mui/material/CircularProgress";
import { scanJobCli } from "@/api";
import { Job, JobStatus, JobPhase, GetScanJobProgressReply, ScanFinding, ScanPreviewOutcome, ScanChange, ScanEntry } from "@/entity";
import { IndexingActions, isJobActive, JobCard, JobProgress, jobProgressFields } from "@/components/job-card";
import { errorMessage } from "@/tools";
import { ReadMediaDialog } from "@/components/read-media-dialog";

export const ScanCard = ({ job }: { job: Job }) => {
  const [visible, setVisible] = useState(false);
  const [error, setError] = useState("");
  const [scan, setScan] = useState(GetScanJobProgressReply.create());
  useEffect(() => {
    if (!visible) return;
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    const refresh = async () => {
      try {
        const reply = await scanJobCli.getProgress({ id: job.id }).response;
        if (active) {
          setScan(GetScanJobProgressReply.create(reply));
          setError("");
        }
      } catch (error) {
        if (active) setError(errorMessage(error, "Could not load scan progress"));
      }
      if (active) timer = setTimeout(() => void refresh(), 2000);
    };
    void refresh();
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [visible, job.id]);
  const display = useMemo(() => {
    const [fields, percentage] = jobProgressFields(job, scan.progress ?? null, "Processed");
    return {
      fields: [
        ...fields,
        { name: "Added", value: Number(scan.added) },
        { name: "Changed", value: Number(scan.changed) },
        { name: "Removed", value: Number(scan.removed) },
        ...(
          [
            ["matched", "Checks passed"],
            ["damaged", "Content mismatch"],
            ["missing", "Missing"],
            ["unreadable", "Unreadable"],
            ["unverifiable", "No baseline"],
            ["previewsReady", "Previews ready"],
            ["previewsSkipped", "Previews skipped"],
            ["previewsFailed", "Previews failed"],
          ] as const
        )
          .filter(([key]) => scan[key] > 0n)
          .map(([key, name]) => ({ name, value: String(scan[key]) })),
      ],
      percentage,
    };
  }, [job, scan]);
  return (
    <JobCard
      job={job}
      onVisibilityChange={setVisible}
      detail={
        <Stack spacing={1}>
          <JobProgress fields={display.fields} indexing={job.status === JobStatus.INDEXING && isJobActive(job)} percentage={display.percentage} />
          {error && <Alert severity="error">{error}</Alert>}
          {scan.scopes
            .filter((scope) => scope.error)
            .map((scope) => (
              <Alert severity="warning" key={`${scope.locationId}:${scope.path}`}>
                {scope.path || "/"}: {scope.error}
              </Alert>
            ))}
        </Stack>
      }
      buttons={
        <>
          {job.status === JobStatus.INDEXING && <IndexingActions job={job} retryLabel="Retry scan" />}
          {job.phase === JobPhase.WAITING_FOR_MEDIA && job.mediaId && (
            <ReadMediaDialog
              mediaIDs={[job.mediaId]}
              operation="scan"
              onRead={async (target) => {
                await scanJobCli.readMedia({ id: job.id, target }).response;
              }}
            />
          )}
          <ScanEntriesDialog jobID={job.id} />
          {(scan.scopes.length > 0 || scan.scopesHasMore) && <ScanEntriesDialog jobID={job.id} scopes />}
        </>
      }
    />
  );
};

const ScanEntriesDialog = ({ jobID, scopes = false }: { jobID: bigint; scopes?: boolean }) => {
  const [open, setOpen] = useState(false);
  const [entries, setEntries] = useState<{ id: bigint; path: string; description: string }[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const request = useRef(0);
  const pending = useRef(false);
  useEffect(() => {
    const generation = request;
    return () => {
      generation.current++;
    };
  }, []);
  const close = () => {
    request.current++;
    pending.current = false;
    setLoading(false);
    setOpen(false);
  };

  const load = async (reset: boolean) => {
    if (pending.current) return;
    pending.current = true;
    const generation = ++request.current;
    setLoading(true);
    setError("");
    try {
      const current = reset ? [] : entries;
      const afterId = current.at(-1)?.id;
      const input = { id: jobID, limit: 200, afterId };
      const reply = scopes
        ? await scanJobCli.listScopes(input).response.then((reply) => ({
            hasMore: reply.hasMore,
            entries: reply.scopes.map((scope) => ({
              id: scope.id,
              path: `${scope.locationId ? `Location ${scope.locationId}` : "Media"} / ${scope.path}`,
              description: scope.error || (scope.publishedAtMs ? "Published" : "Observed"),
            })),
          }))
        : await scanJobCli.listEntries(input).response.then((reply) => ({
            hasMore: reply.hasMore,
            entries: reply.entries.map((entry) => ({ id: entry.id, path: entry.path, description: scanEntryLabel(entry) })),
          }));
      if (generation !== request.current) return;
      setEntries(reset ? reply.entries : [...current, ...reply.entries]);
      setHasMore(reply.hasMore);
    } catch (error) {
      if (generation === request.current) setError(errorMessage(error, "Could not load scan results"));
    } finally {
      if (generation === request.current) {
        pending.current = false;
        setLoading(false);
      }
    }
  };
  const show = () => {
    setOpen(true);
    setEntries([]);
    setHasMore(false);
    void load(true);
  };

  return (
    <Fragment>
      <Button size="small" onClick={show}>
        {scopes ? "Scanned folders" : "Results"}
      </Button>
      {open && (
        <Dialog open onClose={close} maxWidth="md" fullWidth scroll="paper">
          <DialogTitle>{scopes ? "Scanned folders" : "Scan results"}</DialogTitle>
          <DialogContent dividers>
            {loading && <CircularProgress size={20} aria-label="Loading scan results" />}
            {error && (
              <Alert severity="error">
                {error}
                <Button onClick={() => void load(true)}>Reload</Button>
              </Alert>
            )}
            <List disablePadding>
              {entries.map((entry) => (
                <ListItemText key={String(entry.id)} primary={entry.path} secondary={entry.description} />
              ))}
            </List>
            {!loading && !error && entries.length === 0 && <p>No results yet.</p>}
            {hasMore && (
              <Button disabled={loading || !!error} onClick={() => void load(false)}>
                Load More
              </Button>
            )}
          </DialogContent>
          <DialogActions>
            <Button onClick={close}>Close</Button>
          </DialogActions>
        </Dialog>
      )}
    </Fragment>
  );
};

const findingLabels: Record<ScanFinding, string> = {
  [ScanFinding.NOT_CHECKED]: "Not checked",
  [ScanFinding.MATCH]: "Check passed",
  [ScanFinding.MISMATCH]: "Content mismatch",
  [ScanFinding.MISSING]: "Missing",
  [ScanFinding.UNREADABLE]: "Unreadable",
  [ScanFinding.UNVERIFIABLE]: "No verification baseline",
};
const scanEntryLabel = (entry: ScanEntry) =>
  [
    entry.finding !== ScanFinding.NOT_CHECKED ? findingLabels[entry.finding] : (ScanChange[entry.change] ?? "Unknown").toLowerCase(),
    entry.checkedAtMs ? new Date(Number(entry.checkedAtMs)).toLocaleString() : "",
    entry.preview === ScanPreviewOutcome.PREVIEW_READY ? "Preview ready" : "",
    entry.preview === ScanPreviewOutcome.PREVIEW_FAILED ? `Preview failed: ${entry.previewError}` : "",
    entry.preview === ScanPreviewOutcome.PREVIEW_SKIPPED ? "Preview skipped" : "",
    entry.detail,
    entry.stale ? "Observation changed; not published" : "",
  ]
    .filter(Boolean)
    .join(" · ");

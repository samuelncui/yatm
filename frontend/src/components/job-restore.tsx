import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Virtuoso } from "react-virtuoso";

import Button from "@mui/material/Button";
import Alert from "@mui/material/Alert";
import Chip from "@mui/material/Chip";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogTitle from "@mui/material/DialogTitle";
import List from "@mui/material/List";
import ListItemButton from "@mui/material/ListItemButton";
import ListItemText from "@mui/material/ListItemText";
import Stack from "@mui/material/Stack";
import ChevronRightIcon from "@mui/icons-material/ChevronRight";
import ExpandMoreIcon from "@mui/icons-material/ExpandMore";
import { styled } from "@mui/material/styles";

import { cli, restoreJobCli } from "@/api";
import { CopyStatus, Job, JobPhase, JobStatus, Media, Progress, RestoreItem, RestoreMedia, type RestoreSummary } from "@/entity";
import { ReadMediaDialog } from "@/components/read-media-dialog";
import { CancelJobButton, IndexingActions, isJobActive, JobCard, JobProgress, jobProgressFields } from "@/components/job-card";
import { FileRow, FileRowPlaceholder } from "@/components/job-file-list-item";
import { errorMessage } from "@/tools";

const mediaPageSize = 100;

export const RestoreCard = ({ job }: { job: Job }) => {
  const [visible, setVisible] = useState(false);
  const [progress, setProgress] = useState<Progress | null>(null);
  const [summary, setSummary] = useState<RestoreSummary>();
  const [media, setMedia] = useState<RestoreMedia[]>([]);
  const [storage, setStorage] = useState<Map<bigint, Media>>(new Map());
  const [error, setError] = useState("");

  useEffect(() => {
    if (!visible) return;
    let refreshing = false;
    const refresh = async () => {
      if (refreshing) return;
      refreshing = true;
      try {
        const progressResponse = await restoreJobCli.getProgress({ id: job.id }).response;
        setProgress(progressResponse.progress ?? null);
        setSummary(progressResponse.summary);
        if (job.status === JobStatus.INDEXING) return;

        const loaded: RestoreMedia[] = [];
        const names = new Map<bigint, Media>();
        let offset = 0n;
        let hasMore = true;
        while (hasMore) {
          const response = await restoreJobCli.listMedia({
            id: job.id,
            limit: mediaPageSize,
            offset,
            filterStatus: [],
          }).response;
          loaded.push(...response.media);
          if (response.media.length > 0) {
            const catalog = await cli.mediaList({ param: { oneofKind: "mget", mget: { ids: response.media.map((value) => value.mediaId) } } }).response;
            for (const value of catalog.media) names.set(value.id, value);
          }
          hasMore = response.hasMore;
          offset += BigInt(mediaPageSize);
        }
        setMedia(loaded);
        setStorage(names);
        setError("");
      } catch (error) {
        setError(errorMessage(error, "Could not load restore progress"));
      } finally {
        refreshing = false;
      }
    };
    void refresh();
    const timer = setInterval(() => void refresh(), 2000);
    return () => clearInterval(timer);
  }, [visible, job.id, job.status]);

  const [fields, percentage] = useMemo(() => jobProgressFields(job, progress), [job, progress]);
  const indexing = job.status === JobStatus.INDEXING;
  const active = isJobActive(job);
  return (
    <JobCard
      job={job}
      onVisibilityChange={setVisible}
      detail={
        <Fragment>
          <JobProgress fields={fields} indexing={indexing && active} percentage={percentage} />
          {summary && (
            <div className="product-actions" aria-label="Restore results">
              <Chip label={`${summary.verifiedFiles} verified`} color="success" variant="outlined" />
              <Chip label={`${summary.damagedFiles} recovered with damage`} color={summary.damagedFiles > 0n ? "warning" : "default"} variant="outlined" />
              <Chip label={`${summary.unlinkedFiles} not linked`} variant="outlined" />
              <Chip label={`${summary.pendingFiles} pending`} variant="outlined" />
            </div>
          )}
          {error && <Alert severity="warning">{error}</Alert>}
          {!indexing && (
            <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: "wrap", marginTop: 2 }}>
              {media.map((value) => (
                <Chip
                  key={value.mediaId.toString()}
                  label={`${storage.get(value.mediaId)?.name || value.identity}: ${CopyStatus[value.status].toLowerCase()}`}
                  color={value.status === CopyStatus.COMPLETED ? "success" : "primary"}
                  variant="outlined"
                />
              ))}
            </Stack>
          )}
        </Fragment>
      }
      buttons={
        <Fragment>
          {indexing ? (
            <IndexingActions job={job} />
          ) : (
            <Fragment>
              {job.phase === JobPhase.WAITING_FOR_MEDIA && (
                <ReadMediaDialog
                  mediaIDs={media.filter((value) => value.status === CopyStatus.PENDING).map((value) => value.mediaId)}
                  operation="restore"
                  onRead={async (target) => {
                    await restoreJobCli.restoreMedia({ id: job.id, target }).response;
                  }}
                />
              )}
              {active && <CancelJobButton jobID={job.id} />}
              <RestoreViewFilesDialog media={media} jobID={job.id} />
            </Fragment>
          )}
        </Fragment>
      }
    />
  );
};

const MediaRow = styled(ListItemButton)(({ theme }) => ({
  padding: "0.2rem",
  width: "100%",
  position: "sticky",
  top: 0,
  zIndex: 10,
  backgroundColor: theme.palette.background.paper,
}));
const MediaRowText = styled(ListItemText)({ padding: 0, margin: 5, marginLeft: 10 });

const MediaFiles = ({ value, jobID, scrollParent }: { value: RestoreMedia; jobID: bigint; scrollParent: HTMLElement | null }) => {
  const [cache, setCache] = useState<Record<number, RestoreItem>>({});
  const requestSequence = useRef(0);
  const loadRange = useCallback(
    async ({ startIndex, endIndex }: { startIndex: number; endIndex: number }) => {
      const request = ++requestSequence.current;
      const response = await restoreJobCli.listFiles({
        id: jobID,
        mediaId: value.mediaId,
        limit: endIndex - startIndex + 1,
        offset: BigInt(startIndex),
        filterStatus: [],
      }).response;
      if (request !== requestSequence.current) return;
      const next: Record<number, RestoreItem> = {};
      response.items.forEach((item, index) => {
        next[startIndex + index] = item;
      });
      setCache(next);
    },
    [jobID, value.mediaId],
  );

  return (
    <Virtuoso
      style={{ width: "100%" }}
      useWindowScroll
      customScrollParent={scrollParent ?? undefined}
      totalCount={Number(value.total)}
      defaultItemHeight={54}
      rangeChanged={loadRange}
      itemContent={(index) => {
        const item = cache[index];
        if (!item?.candidate) return <FileRowPlaceholder indent={27} />;
        return (
          <FileRow
            src={{
              path: item.file?.targetPath || item.candidate.mediaPath,
              size: item.size,
              status: item.status,
              resultLabel: item.damaged
                ? "Recovered with damage"
                : item.status === CopyStatus.COMPLETED
                  ? item.linked
                    ? "Verified · Linked"
                    : "Verified · Not linked"
                  : undefined,
              resultMessage: item.resultMessage,
              resultFileID: item.resultFileId,
            }}
            indent={27}
          />
        );
      }}
    />
  );
};

const RestoreViewFilesDialog = ({ media, jobID }: { media: RestoreMedia[]; jobID: bigint }) => {
  const [open, setOpen] = useState(false);
  const [openedMediaID, setOpenedMediaID] = useState<bigint | null>(null);
  const [scrollParent, setScrollParent] = useState<HTMLElement | null>(null);
  const activeMediaID = media.some((value) => value.mediaId === openedMediaID) ? openedMediaID : null;

  return (
    <Fragment>
      <Button size="small" onClick={() => setOpen(true)}>
        View Files
      </Button>
      {open && (
        <Dialog open onClose={() => setOpen(false)} maxWidth="lg" fullWidth scroll="paper" className="job-view-dialog">
          <DialogTitle>View Files</DialogTitle>
          <DialogContent dividers style={{ padding: 0 }} ref={setScrollParent}>
            <List style={{ width: "100%", padding: 0 }}>
              {media.map((value) => {
                const opened = activeMediaID === value.mediaId;
                return (
                  <Fragment key={value.mediaId.toString()}>
                    <MediaRow onClick={() => setOpenedMediaID(opened ? null : value.mediaId)}>
                      {opened ? <ExpandMoreIcon /> : <ChevronRightIcon />}
                      <MediaRowText primary={`Media: ${value.identity}`} secondary={`Files: ${value.total} | Status: ${CopyStatus[value.status]}`} />
                    </MediaRow>
                    {opened && <MediaFiles value={value} jobID={jobID} scrollParent={scrollParent} />}
                  </Fragment>
                );
              })}
            </List>
          </DialogContent>
          <DialogActions>
            <Button onClick={() => setOpen(false)}>Close</Button>
          </DialogActions>
        </Dialog>
      )}
    </Fragment>
  );
};

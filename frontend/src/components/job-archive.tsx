import { Fragment, ChangeEvent, useRef, useState, useMemo, useCallback, useContext, useEffect, memo } from "react";
import { assert } from "@protobuf-ts/runtime";
import format from "format-duration";

import { Virtuoso, VirtuosoHandle } from "react-virtuoso";

import { styled } from "@mui/material/styles";
import Grid from "@mui/material/Grid";
import Box from "@mui/material/Box";
import ListItem from "@mui/material/ListItem";
import ListItemButton from "@mui/material/ListItemButton";
import ListItemText from "@mui/material/ListItemText";
import Typography from "@mui/material/Typography";
import Button from "@mui/material/Button";
import TextField from "@mui/material/TextField";
import MenuItem from "@mui/material/MenuItem";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogContentText from "@mui/material/DialogContentText";
import DialogTitle from "@mui/material/DialogTitle";
import LinearProgress from "@mui/material/LinearProgress";

import { cli } from "../api";
import { Job, JobDispatchRequest, CopyStatus, SourceState, JobStatus } from "../entity";
import { JobArchiveCopyingParam, JobArchiveStep, JobArchiveDisplay, JobArchiveState } from "../entity";

import { formatFilesize, sleep } from "../tools";

import { JobCard } from "./job-card";
import { RefreshContext } from "../pages/jobs";
import { FileListItem } from "./job-file-list-item";

export const ArchiveCard = ({ job, state, display }: { job: Job; state: JobArchiveState; display: JobArchiveDisplay | null }): JSX.Element => {
  const [fields, progress] = useMemo(() => {
    const totalFiles = state.sources.length;
    let submitedFiles = 0,
      submitedBytes = 0,
      totalBytes = 0;
    for (const file of state.sources) {
      totalBytes += Number(file.size);
      if (file.status !== CopyStatus.SUBMITED) {
        continue;
      }
      submitedFiles++;
      submitedBytes += Number(file.size);
    }

    const copiedFiles = submitedFiles + Number(display?.copiedFiles || 0n);
    const copiedBytes = submitedBytes + Number(display?.copiedBytes || 0n);
    const avgSpeed = (() => {
      if (!display || !display.copiedBytes || !display.startTime) {
        return NaN;
      }

      const duration = Date.now() / 1000 - Number(display.startTime);
      if (duration <= 0) {
        return NaN;
      }

      return Number(display.copiedBytes) / duration;
    })();

    const progress = (totalBytes > 0 ? copiedBytes / totalBytes : 1) * 100;
    const fields = [
      { name: "Current Step", value: JobArchiveStep[state.step] },
      { name: "Current Speed", value: display?.speed ? `${formatFilesize(display?.speed)}/s` : "--" },
      { name: "Average Speed", value: !isNaN(avgSpeed) ? `${formatFilesize(avgSpeed)}/s` : "--" },
      { name: "Estimated Time", value: !isNaN(avgSpeed) ? format(((totalBytes - copiedBytes) * 1000) / avgSpeed) : "--" },
      { name: "Copied Files", value: copiedFiles },
      { name: "Copied Bytes", value: formatFilesize(copiedBytes) },
      { name: "Submited Files", value: submitedFiles },
      { name: "Submited Bytes", value: formatFilesize(submitedBytes) },
      { name: "Total Files", value: totalFiles },
      { name: "Total Bytes", value: formatFilesize(totalBytes) },
    ];

    return [fields, progress];
  }, [state, display]);

  return (
    <JobCard
      job={job}
      detail={
        <Grid container spacing={2}>
          <Grid item xs={12}>
            <Box sx={{ paddingTop: "1em" }}>
              <LinearProgress variant="determinate" value={progress} />
            </Box>
          </Grid>
          {fields.map((field, idx) => (
            <Grid item xs={12} md={3} key={idx}>
              <Typography variant="body1">
                <b>{field.name}</b>: {field.value}
              </Typography>
            </Grid>
          ))}
        </Grid>
      }
      buttons={
        <Fragment>
          {state.step === JobArchiveStep.WAIT_FOR_TAPE && <NewTapeDialog key="NEW_TAPE" job={job} />}
          {job.status !== JobStatus.PROCESSING && <RollbackDialog key="ROLLBACK" jobID={job.id} state={state} />}
          <ArchiveViewFilesDialog key="ARCHIVE_VIEW_FILES" sources={state.sources} />
        </Fragment>
      }
    />
  );
};

const NewTapeDialog = ({ job }: { job: Job }) => {
  const refresh = useContext(RefreshContext);

  const [devices, setDevices] = useState<string[]>([]);
  const [param, setParam] = useState<JobArchiveCopyingParam | null>(null);
  const handleClickOpen = async () => {
    const reply = await cli.deviceList({}).response;
    setDevices(reply.devices);
    setParam(JobArchiveCopyingParam.create());
  };
  const handleClose = () => {
    setParam(null);
    setDevices([]);
  };
  const handleChange = (key: keyof JobArchiveCopyingParam) => (event: ChangeEvent<HTMLInputElement>) => {
    if (param === null) {
      return;
    }
    setParam({ ...param, [key]: event.target.value });
  };
  const handleSubmit = async () => {
    if (!param) {
      return;
    }

    const trimedParam: JobArchiveCopyingParam = {
      device: param.device,
      barcode: param.barcode.toUpperCase(),
      name: param.name,
    };
    assert(trimedParam.barcode.length === 6);

    const req = makeArchiveCopyingParam(job.id, trimedParam);
    console.log("job dispatch start, request= ", req);

    const reply = await cli.jobDispatch(req).response;
    console.log("job dispatch success, reply= ", reply);
    await refresh();
    handleClose();
  };

  return (
    <Fragment>
      <Button size="small" onClick={handleClickOpen}>
        Load Tape
      </Button>
      {param && (
        <Dialog open={true} onClose={handleClose} maxWidth={"sm"} fullWidth>
          <DialogTitle>Load Tape</DialogTitle>
          <DialogContent>
            <DialogContentText>After load tape into tape drive, click 'Submit'</DialogContentText>
            <TextField select required margin="dense" label="Drive Device" fullWidth variant="standard" value={param.device} onChange={handleChange("device")}>
              {devices.map((device) => (
                <MenuItem key={device} value={device}>
                  {device}
                </MenuItem>
              ))}
            </TextField>
            <TextField required margin="dense" label="Tape Barcode" fullWidth variant="standard" value={param.barcode} onChange={handleChange("barcode")} />
            <TextField required margin="dense" label="Tape Name" fullWidth variant="standard" value={param.name} onChange={handleChange("name")} />
          </DialogContent>
          <DialogActions>
            <Button onClick={handleClose}>Cancel</Button>
            <Button onClick={handleSubmit}>Submit</Button>
          </DialogActions>
        </Dialog>
      )}
    </Fragment>
  );
};

const ArchiveViewFilesDialog = ({ sources }: { sources: SourceState[] }) => {
  const [open, setOpen] = useState(false);
  const handleClickOpen = () => {
    setOpen(true);
  };
  const handleClose = () => {
    setOpen(false);
  };

  return (
    <Fragment>
      <Button size="small" onClick={handleClickOpen}>
        View Files
      </Button>
      {open && <FileList title="View Files" onClose={handleClose} sources={sources} />}
    </Fragment>
  );
};

const RollbackDialog = ({ jobID, state }: { jobID: bigint; state: JobArchiveState }) => {
  const [open, setOpen] = useState(false);
  const handleClickOpen = () => {
    setOpen(true);
  };
  const handleClose = () => {
    setOpen(false);
  };

  return (
    <Fragment>
      <Button size="small" onClick={handleClickOpen}>
        Rollback
      </Button>
      {open && <RollbackFileList onClose={handleClose} jobID={jobID} state={state} />}
    </Fragment>
  );
};

const RollbackFileList = ({ onClose, jobID, state }: { onClose: () => void; jobID: bigint; state: JobArchiveState }) => {
  const refresh = useContext(RefreshContext);
  const handleClickItem = useCallback(
    async (idx: number) => {
      const found = state.sources[idx];
      if (!found || !found.source) {
        return;
      }

      const path = found.source.base + found.source.path.join("/");
      if (!confirm(`Rollback to file '${path}', all files after this file (included) will be set to 'PENDING'.`)) {
        return;
      }

      const sources = Array.from(state.sources);
      for (let i = idx; i < sources.length; i++) {
        sources[i].status = CopyStatus.PENDING;
      }

      await cli.jobEditState({ id: jobID, state: { state: { oneofKind: "archive", archive: { ...state, sources } } } });
      await refresh();

      alert(`Rollback to file '${path}' success!`);
    },
    [state, refresh],
  );

  return <FileList title="Click Rollback Target File" onClose={onClose} onClickItem={handleClickItem} sources={state.sources} />;
};

const FileList = memo(
  ({ onClose, onClickItem, title, sources }: { onClose: () => void; onClickItem?: (idx: number) => void; title: string; sources: SourceState[] }) => {
    const virtuosoRef = useRef<VirtuosoHandle | null>(null);

    useEffect(() => {
      (async () => {
        const idx = sources.findIndex((src) => src.status !== CopyStatus.SUBMITED && src.status !== CopyStatus.STAGED);
        if (idx < 0) {
          return;
        }

        await sleep(100);
        if (!virtuosoRef.current) {
          return;
        }

        virtuosoRef.current.scrollToIndex({ index: idx, align: "center", behavior: "smooth" });
      })();
    }, [sources]);

    return (
      <Dialog open={true} onClose={onClose} maxWidth={"lg"} fullWidth scroll="paper" sx={{ height: "100%" }} className="view-log-dialog">
        <DialogTitle>{title}</DialogTitle>
        <DialogContent dividers style={{ padding: 0 }}>
          <Virtuoso
            style={{ width: "100%", height: "100%" }}
            totalCount={sources.length}
            ref={virtuosoRef}
            itemContent={(idx) => {
              const src = sources[idx];
              if (!src || !src.source) {
                return null;
              }

              return (
                <FileListItem
                  src={{ path: src.source.base + src.source.path.join("/"), size: src.size, status: src.status }}
                  onClick={onClickItem ? () => onClickItem(idx) : undefined}
                />
              );
            }}
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={onClose}>Close</Button>
        </DialogActions>
      </Dialog>
    );
  },
);

function makeArchiveCopyingParam(jobID: bigint, param: JobArchiveCopyingParam): JobDispatchRequest {
  return {
    id: jobID,
    param: {
      param: {
        oneofKind: "archive",
        archive: {
          param: {
            oneofKind: "copying",
            copying: param,
          },
        },
      },
    },
  };
}

import { Fragment, ChangeEvent } from "react";
import { useState, useRef, useEffect, useMemo, useCallback, FC } from "react";
import { assert } from "@protobuf-ts/runtime";
import format from "format-duration";

import Grid from "@mui/material/Grid";
import Box from "@mui/material/Box";
import List from "@mui/material/List";
import ListItemButton from "@mui/material/ListItemButton";
import ListItemText from "@mui/material/ListItemText";
import Typography from "@mui/material/Typography";

import Card from "@mui/material/Card";
import CardActions from "@mui/material/CardActions";
import CardContent from "@mui/material/CardContent";

import Button from "@mui/material/Button";
import TextField from "@mui/material/TextField";
import MenuItem from "@mui/material/MenuItem";

import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogContentText from "@mui/material/DialogContentText";
import DialogTitle from "@mui/material/DialogTitle";
import LinearProgress from "@mui/material/LinearProgress";
import Divider from "@mui/material/Divider";

import "./app.less";
import { cli, sleep } from "./api";
import { Job, JobDisplay, JobCreateRequest, JobListRequest, JobNextRequest, JobStatus, CopyStatus } from "./entity";
import { JobArchiveCopyingParam, JobArchiveStep, JobDisplayArchive, JobParamArchive, JobStateArchive } from "./entity";
import { SourceState } from "./entity";

import { formatFilesize } from "./tools";

export const JobsType = "jobs";
type DisplayableJob = Job & Partial<JobDisplay>;

export const JobsBrowser = () => {
  const [jobs, setJobs] = useState<DisplayableJob[]>([]);
  const refresh = useCallback(async () => {
    const jobReplys = await cli.jobList(JobListRequest.create({ param: { oneofKind: "list", list: {} } })).response;
    const displayReplys = await Promise.all(jobReplys.jobs.map((job) => cli.jobDisplay({ id: job.id }).response));
    const targets = jobReplys.jobs.map((job, idx) => ({ ...job, ...displayReplys[idx].display }));
    console.log("refresh jobs list, ", targets);
    setJobs(targets);
  }, [setJobs]);
  useEffect(() => {
    refresh();
    const timer = setInterval(refresh, 2000);
    return () => {
      clearInterval(timer);
    };
  }, []);

  return (
    <Box className="browser-box">
      <Grid className="browser-container" container>
        <Grid className="browser" item xs={2}>
          <List
            sx={{
              width: "100%",
              height: "100%",
              bgcolor: "background.paper",
              boxSizing: "border-box",
            }}
            component="nav"
            // subheader={
            //   <ListSubheader component="div" id="nested-list-subheader">
            //     Nested List Items
            //   </ListSubheader>
            // }
          >
            <NewArchiveDialog refresh={refresh} />
          </List>
        </Grid>
        <Grid className="browser" item xs={10}>
          <div className="job-list">
            {jobs.map((job) => (
              <GetJobCard job={job} key={job.id.toString()} refresh={refresh} />
            ))}
          </div>
        </Grid>
      </Grid>
    </Box>
  );
};

const GetJobCard = ({ job, refresh }: { job: DisplayableJob; refresh: () => Promise<void> }): JSX.Element => {
  if (!job.state) {
    return <JobCard job={job} />;
  }

  const type = job.state?.state.oneofKind;
  switch (type) {
    case "archive":
      return (
        <ArchiveCard job={job} refresh={refresh} state={job.state.state.archive} display={job.display?.oneofKind === "archive" ? job.display.archive : null} />
      );
    default:
      return <JobCard job={job} />;
  }
};

type ArchiveLastDisplay = { copyedBytes: bigint; lastUpdate: number };

const ArchiveCard = ({
  job,
  state,
  display,
  refresh,
}: {
  job: Job;
  state: JobStateArchive;
  display: JobDisplayArchive | null;
  refresh: () => Promise<void>;
}): JSX.Element => {
  const [fields, progress] = useMemo(() => {
    const totalFiles = state.sources.length;
    let submitedFiles = 0,
      submitedBytes = 0,
      totalBytes = 0;
    for (const file of state.sources) {
      totalBytes += Number(file.size);
      if (file.status !== CopyStatus.Submited) {
        continue;
      }
      submitedFiles++;
      submitedBytes += Number(file.size);
    }

    const copyedFiles = submitedFiles + Number(display?.copyedFiles || 0n);
    const copyedBytes = submitedBytes + Number(display?.copyedBytes || 0n);
    const avgSpeed = (() => {
      if (!display || !display.copyedBytes || !display.startTime) {
        return NaN;
      }

      const duration = Date.now() / 1000 - Number(display.startTime);
      if (duration <= 0) {
        return NaN;
      }

      return Number(display.copyedBytes) / duration;
    })();

    const progress = (totalBytes > 0 ? copyedBytes / totalBytes : 1) * 100;
    const fields = [
      { name: "Current Step", value: JobArchiveStep[state.step] },
      { name: "Current Speed", value: display?.speed ? `${formatFilesize(display?.speed)}/s` : "--" },
      { name: "Average Speed", value: !isNaN(avgSpeed) ? `${formatFilesize(avgSpeed)}/s` : "--" },
      { name: "Estimated Time", value: !isNaN(avgSpeed) ? format(((totalBytes - copyedBytes) * 1000) / avgSpeed) : "--" },
      { name: "Total Files", value: totalFiles },
      { name: "Total Bytes", value: formatFilesize(totalBytes) },
      { name: "Submited Files", value: submitedFiles },
      { name: "Submited Bytes", value: formatFilesize(submitedBytes) },
      { name: "Copyed Files", value: copyedFiles },
      { name: "Copyed Bytes", value: formatFilesize(copyedBytes) },
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
          {state.step === JobArchiveStep.WaitForTape && <LoadTapeDialog job={job} refresh={refresh} />}
          <ViewLogDialog jobID={job.id} />
          <ArchiveViewFilesDialog sources={state.sources} />
        </Fragment>
      }
    />
  );
};

const NewArchiveDialog = ({ refresh }: { refresh: () => Promise<void> }) => {
  const [open, setOpen] = useState(false);
  const handleClickOpen = () => {
    setOpen(true);
  };
  const handleClose = () => {
    setOpen(false);
  };

  const [source, setSource] = useState("");
  const handleSubmit = async () => {
    let path = source.trim();
    if (path.length === 0) {
      return;
    }

    while (path.endsWith("/")) {
      path = path.slice(0, -1);
    }

    const splitIdx = path.lastIndexOf("/");
    if (splitIdx < 0) {
      return;
    }

    console.log(await cli.jobCreate(makeArchiveParam(1n, { sources: [{ base: path.slice(0, splitIdx + 1), path: [path.slice(splitIdx + 1)] }] })).response);
    await refresh();
    handleClose();
  };

  return (
    <Fragment>
      <ListItemButton onClick={handleClickOpen}>
        <ListItemText primary="New Archive Job" />
      </ListItemButton>
      {open && (
        <Dialog open={true} onClose={handleClose} maxWidth={"sm"} fullWidth>
          <DialogTitle>New Archive Job</DialogTitle>
          <DialogContent>
            <TextField
              autoFocus
              margin="dense"
              label="Source Path"
              fullWidth
              variant="standard"
              value={source}
              onChange={(event: ChangeEvent<HTMLInputElement>) => setSource(event.target.value)}
            />
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

const LoadTapeDialog = ({ job, refresh }: { job: Job; refresh: () => Promise<void> }) => {
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

    const reply = await cli.jobNext(makeArchiveCopyingParam(job.id, trimedParam)).response;
    console.log("job next reply= ", reply);
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

const ViewLogDialog = ({ jobID }: { jobID: bigint }) => {
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
        View Log
      </Button>
      {open && (
        <Dialog open={true} onClose={handleClose} maxWidth={"lg"} fullWidth scroll="paper" sx={{ height: "100%" }} className="view-log-dialog">
          <DialogTitle>View Log</DialogTitle>
          <DialogContent dividers>
            <LogConsole jobId={jobID} />
          </DialogContent>
          <DialogActions>
            <Button onClick={handleClose}>Close</Button>
          </DialogActions>
        </Dialog>
      )}
    </Fragment>
  );
};

const LogConsole = ({ jobId }: { jobId: bigint }) => {
  const [log, setLog] = useState<string>("");
  const bottom = useRef(null);
  const refreshLog = useCallback(async () => {
    const reply = await cli.jobGetLog({ jobId, offset: BigInt(log.length) }).response;
    setLog(log + new TextDecoder().decode(reply.logs));

    if (log.length === 0 && reply.logs.length > 0 && bottom && bottom.current) {
      await sleep(10);
      (bottom.current as HTMLElement).scrollIntoView(true);
      await sleep(10);
      (bottom.current as HTMLElement).parentElement?.scrollBy(0, 100);
    }
  }, [log, setLog, bottom]);
  useEffect(() => {
    let closed = false;
    (async () => {
      while (!closed) {
        await refreshLog();
        await sleep(2000);
      }
    })();

    return () => {
      closed = true;
    };
  }, [refreshLog]);

  return (
    <Fragment>
      <pre>{log || "loading..."}</pre>
      <div ref={bottom} />
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
      {open && (
        <Dialog open={true} onClose={handleClose} maxWidth={"lg"} fullWidth scroll="paper" sx={{ height: "100%" }} className="view-log-dialog">
          <DialogTitle>View Files</DialogTitle>
          <DialogContent dividers>
            {sources.map((src) => {
              if (!src.source) {
                return null;
              }
              return (
                <ListItemText
                  primary={src.source.base + src.source.path.join("/")}
                  secondary={`Size: ${formatFilesize(src.size)} Status: ${CopyStatus[src.status]}`}
                />
              );
            })}
          </DialogContent>
          <DialogActions>
            <Button onClick={handleClose}>Close</Button>
          </DialogActions>
        </Dialog>
      )}
    </Fragment>
  );
};

const JobCard = ({ job, detail, buttons }: { job: Job; detail?: JSX.Element; buttons?: JSX.Element }) => {
  return (
    <Card sx={{ textAlign: "left" }} className="job-detail">
      <CardContent>
        <Typography sx={{ fontSize: 14 }} color="text.secondary" gutterBottom>
          {`${JobStatus[job.status]}`}
        </Typography>
        <Typography variant="h5" component="div">{`${job.state?.state.oneofKind?.toUpperCase()} Job ${job.id}`}</Typography>
        {detail}
      </CardContent>
      <Divider />
      <CardActions>{buttons}</CardActions>
    </Card>
  );
};

function makeArchiveParam(priority: bigint, param: JobParamArchive): JobCreateRequest {
  return {
    job: {
      priority,
      param: {
        param: {
          oneofKind: "archive",
          archive: param,
        },
      },
    },
  };
}

function makeArchiveCopyingParam(jobID: bigint, param: JobArchiveCopyingParam): JobNextRequest {
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

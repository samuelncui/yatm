import { Fragment, ChangeEvent } from "react";
import { useState, useRef, useEffect, useMemo, useCallback, FC } from "react";
import { createContext, useContext } from "react";
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
import Chip, { ChipProps } from "@mui/material/Chip";
import Stack from "@mui/material/Stack";

import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogContentText from "@mui/material/DialogContentText";
import DialogTitle from "@mui/material/DialogTitle";
import LinearProgress from "@mui/material/LinearProgress";
import Divider from "@mui/material/Divider";

import { TreeView, TreeItem } from "@mui/x-tree-view";

import ExpandMoreIcon from "@mui/icons-material/ExpandMore";
import ChevronRightIcon from "@mui/icons-material/ChevronRight";

import { cli, fileBase } from "../api";
import { sleep } from "../tools";
import { Job, JobDisplay, JobListRequest, JobNextRequest, JobStatus, CopyStatus, LibraryEntityType, JobDeleteRequest } from "../entity";

import { JobArchiveCopyingParam, JobArchiveStep, JobArchiveDisplay, JobArchiveState } from "../entity";
import { SourceState } from "../entity";

import { JobRestoreCopyingParam, JobRestoreStep, JobRestoreDisplay, JobRestoreState } from "../entity";
import { RestoreTape } from "../entity";

import { formatFilesize, download } from "../tools";

export const JobsType = "jobs";
type DisplayableJob = Job & Partial<JobDisplay>;

const RefreshContext = createContext<() => Promise<void>>(async () => {});

export const JobsBrowser = () => {
  const [jobs, setJobs] = useState<DisplayableJob[] | null>(null);
  const refresh = useCallback(async () => {
    const jobReplys = await cli.jobList(JobListRequest.create({ param: { oneofKind: "list", list: {} } })).response;
    const displays = new Map<BigInt, JobDisplay>();
    for (const reply of await Promise.all(
      jobReplys.jobs
        .filter((job) => job.status === JobStatus.PROCESSING)
        .map((job) => cli.jobDisplay({ id: job.id }).response.then((reply) => ({ ...reply, jobID: job.id }))),
    )) {
      if (!reply.display) {
        continue;
      }

      displays.set(reply.jobID, reply.display);
    }

    const targets = jobReplys.jobs.map((job) => ({ ...job, ...displays.get(job.id) }));
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
    <RefreshContext.Provider value={refresh}>
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
              {/* <NewArchiveDialog refresh={refresh} /> */}
              <ListItemButton
                onClick={async () => {
                  const resp = await cli.libraryExport({ types: [LibraryEntityType.FILE, LibraryEntityType.TAPE, LibraryEntityType.POSITION] }).response;
                  download(resp.json, "database.json", "application/json");
                }}
              >
                <ListItemText primary="Export Database" />
              </ListItemButton>
              <ImportDatabaseDialog />
            </List>
          </Grid>
          <Grid className="browser" item xs={10}>
            <div className="job-list">{jobs ? jobs.map((job) => <GetJobCard job={job} key={job.id.toString()} refresh={refresh} />) : <LinearProgress />}</div>
          </Grid>
        </Grid>
      </Box>
    </RefreshContext.Provider>
  );
};

const ImportDatabaseDialog = () => {
  const [open, setOpen] = useState<boolean>(false);
  const [file, setFile] = useState<File | null>(null);
  const handleClickOpen = async () => {
    setOpen(true);
  };
  const handleClose = () => {
    setOpen(false);
    setFile(null);
  };

  const handleChange = (event: ChangeEvent<HTMLInputElement>) => {
    if (!event.target.files) {
      return;
    }
    if (event.target.files.length === 0) {
      return;
    }

    setFile(event.target.files[0]);
  };
  const handleSubmit = async () => {
    if (!file) {
      return;
    }

    const resp = await fetch(fileBase + "/library/_import", {
      body: file,
      method: "POST",
    });
    console.log(await resp.json());
    handleClose();
  };

  return (
    <Fragment>
      <ListItemButton onClick={handleClickOpen}>
        <ListItemText primary="Import Database" />
      </ListItemButton>
      {open && (
        <Dialog open={true} onClose={handleClose} maxWidth={"sm"} fullWidth>
          <DialogTitle>Load Tape</DialogTitle>
          <DialogContent>
            <Button variant="contained" component="label">
              Upload File
              <input type="file" onChange={handleChange} hidden />
            </Button>
            {file && <p>{file.name}</p>}
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
    case "restore":
      return (
        <RestoreCard job={job} refresh={refresh} state={job.state.state.restore} display={job.display?.oneofKind === "restore" ? job.display.restore : null} />
      );
    default:
      return <JobCard job={job} />;
  }
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

const ArchiveCard = ({
  job,
  state,
  display,
  refresh,
}: {
  job: Job;
  state: JobArchiveState;
  display: JobArchiveDisplay | null;
  refresh: () => Promise<void>;
}): JSX.Element => {
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
          {state.step === JobArchiveStep.WAIT_FOR_TAPE && <NewTapeDialog job={job} refresh={refresh} />}
          <ViewLogDialog jobID={job.id} />
          <ArchiveViewFilesDialog sources={state.sources} />
        </Fragment>
      }
    />
  );
};

const RestoreViewFilesDialog = ({ tapes }: { tapes: RestoreTape[] }) => {
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
            <TreeView defaultCollapseIcon={<ExpandMoreIcon />} defaultExpandIcon={<ChevronRightIcon />}>
              {tapes.map((tape) => {
                if (!tape.files) {
                  return null;
                }

                return (
                  <TreeItem label={tape.barcode} nodeId={`tape-${tape.tapeId}`}>
                    {tape.files.map((file) => (
                      <TreeItem
                        label={
                          <pre style={{ margin: 0 }}>
                            {file.tapePath} <b>{CopyStatus[file.status]}</b>
                          </pre>
                        }
                        nodeId={`file-${file.positionId}`}
                      />
                    ))}
                  </TreeItem>
                );
              })}
            </TreeView>
          </DialogContent>
          <DialogActions>
            <Button onClick={handleClose}>Close</Button>
          </DialogActions>
        </Dialog>
      )}
    </Fragment>
  );
};

const tapeStatusToColor = (status: CopyStatus): ChipProps["color"] => {
  switch (status) {
    case CopyStatus.DRAFT:
      return "primary";
    case CopyStatus.PENDING:
      return "primary";
    case CopyStatus.RUNNING:
      return "secondary";
    case CopyStatus.STAGED:
      return "warning";
    case CopyStatus.SUBMITED:
      return "success";
    case CopyStatus.FAILED:
      return "error";
    default:
      return "default";
  }
};

const RestoreCard = ({
  job,
  state,
  display,
  refresh,
}: {
  job: Job;
  state: JobRestoreState;
  display: JobRestoreDisplay | null;
  refresh: () => Promise<void>;
}): JSX.Element => {
  const [fields, progress] = useMemo(() => {
    const totalFiles = state.tapes.reduce((count, tape) => count + tape.files.length, 0);
    let successFiles = 0,
      successBytes = 0,
      copiedFiles = Number(display?.copiedFiles || 0n),
      copiedBytes = Number(display?.copiedBytes || 0n),
      totalBytes = 0;
    for (const tape of state.tapes) {
      for (const file of tape.files) {
        totalBytes += Number(file.size);

        if (file.status === CopyStatus.SUBMITED || file.status === CopyStatus.STAGED) {
          successFiles++;
          successBytes += Number(file.size);
        }

        if (file.status === CopyStatus.SUBMITED) {
          copiedFiles++;
          copiedBytes += Number(file.size);
        }
      }
    }

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
      { name: "Success Files", value: successFiles },
      { name: "Success Bytes", value: formatFilesize(successBytes) },
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
          <Grid item xs={12} md={12}>
            <Stack direction="row" spacing={1}>
              {state.tapes.map((tape) => (
                <Chip label={`${tape.barcode}: ${CopyStatus[tape.status]}`} color={tapeStatusToColor(tape.status)} variant="outlined" key={`${tape.tapeId}`} />
              ))}
            </Stack>
          </Grid>
        </Grid>
      }
      buttons={
        <Fragment>
          {state.step === JobRestoreStep.WAIT_FOR_TAPE && <LoadTapeDialog job={job} refresh={refresh} />}
          <ViewLogDialog jobID={job.id} />
          <RestoreViewFilesDialog tapes={state.tapes} />
        </Fragment>
      }
    />
  );
};

const NewTapeDialog = ({ job, refresh }: { job: Job; refresh: () => Promise<void> }) => {
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

const LoadTapeDialog = ({ job, refresh }: { job: Job; refresh: () => Promise<void> }) => {
  const [devices, setDevices] = useState<string[] | null>(null);
  const [device, setDevice] = useState<string | null>(null);
  const handleClickOpen = async () => {
    const reply = await cli.deviceList({}).response;
    setDevices(reply.devices);
  };
  const handleClose = () => {
    setDevices(null);
    setDevice(null);
  };

  const handleChange = (event: ChangeEvent<HTMLInputElement>) => {
    setDevice(event.target.value);
  };
  const handleSubmit = async () => {
    if (!device) {
      return;
    }

    const trimedParam: JobRestoreCopyingParam = {
      device: device,
    };

    const reply = await cli.jobNext(makeRestoreCopyingParam(job.id, trimedParam)).response;
    console.log("job next reply= ", reply);
    await refresh();
    handleClose();
  };

  return (
    <Fragment>
      <Button size="small" onClick={handleClickOpen}>
        Load Tape
      </Button>
      {devices && (
        <Dialog open={true} onClose={handleClose} maxWidth={"sm"} fullWidth>
          <DialogTitle>Load Tape</DialogTitle>
          <DialogContent>
            <DialogContentText>After load tape into tape drive, click 'Submit'</DialogContentText>
            <TextField select required margin="dense" label="Drive Device" fullWidth variant="standard" value={device} onChange={handleChange}>
              {devices.map((device) => (
                <MenuItem key={device} value={device}>
                  {device}
                </MenuItem>
              ))}
            </TextField>
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

const DeleteJobButton = ({ jobID }: { jobID: bigint }) => {
  const refresh = useContext(RefreshContext);
  const deleteJob = useCallback(async () => {
    await cli.jobDelete(JobDeleteRequest.create({ ids: [jobID] }));
    await refresh();
  }, [jobID]);

  return (
    <Button size="small" onClick={deleteJob} style={{ marginLeft: "auto", marginRight: 0 }}>
      Delete Job
    </Button>
  );
};

const JobCard = ({ job, detail, buttons }: { job: Job; detail?: JSX.Element; buttons?: JSX.Element }) => {
  return (
    <Card sx={{ textAlign: "left" }} className="job-detail">
      <CardContent>
        <Typography sx={{ fontSize: 14 }} color="text.secondary" gutterBottom>
          {`${JobStatus[job.status]}`}
        </Typography>
        <Typography variant="h5" component="div">{`Job ${job.id} - ${job.state?.state.oneofKind?.toUpperCase()}`}</Typography>
        {detail}
      </CardContent>
      <Divider />
      <CardActions>
        {buttons}
        <DeleteJobButton jobID={job.id} />
      </CardActions>
    </Card>
  );
};

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

function makeRestoreCopyingParam(jobID: bigint, param: JobRestoreCopyingParam): JobNextRequest {
  return {
    id: jobID,
    param: {
      param: {
        oneofKind: "restore",
        restore: {
          param: {
            oneofKind: "copying",
            copying: param,
          },
        },
      },
    },
  };
}

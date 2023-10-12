import { Fragment, ChangeEvent, useState, useMemo, useContext } from "react";
import format from "format-duration";

import Grid from "@mui/material/Grid";
import Box from "@mui/material/Box";
import Typography from "@mui/material/Typography";
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
import ExpandMoreIcon from "@mui/icons-material/ExpandMore";
import ChevronRightIcon from "@mui/icons-material/ChevronRight";

import { TreeView, TreeItem } from "@mui/x-tree-view";

import { cli } from "../api";
import { Job, JobDispatchRequest, CopyStatus, JobArchiveStep } from "../entity";
import { JobRestoreCopyingParam, JobRestoreStep, JobRestoreDisplay, JobRestoreState } from "../entity";
import { RestoreTape } from "../entity";

import { formatFilesize } from "../tools";

import { JobCard } from "./job-card";
import { RefreshContext } from "../pages/jobs";

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

export const RestoreCard = ({ job, state, display }: { job: Job; state: JobRestoreState; display: JobRestoreDisplay | null }): JSX.Element => {
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
            <Stack direction="row" spacing={1} style={{ flexWrap: "wrap" }}>
              {state.tapes.map((tape) => (
                <Chip label={`${tape.barcode}: ${CopyStatus[tape.status]}`} color={tapeStatusToColor(tape.status)} variant="outlined" key={`${tape.tapeId}`} />
              ))}
            </Stack>
          </Grid>
        </Grid>
      }
      buttons={
        <Fragment>
          {state.step === JobRestoreStep.WAIT_FOR_TAPE && <LoadTapeDialog key="LOAD_TAPE" job={job} />}
          <RestoreViewFilesDialog key="RESTORE_VIEW_FILES" tapes={state.tapes} />
        </Fragment>
      }
    />
  );
};

const LoadTapeDialog = ({ job }: { job: Job }) => {
  const refresh = useContext(RefreshContext);

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

    const req = makeRestoreCopyingParam(job.id, trimedParam);
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

const RestoreViewFilesDialog = ({ tapes }: { tapes: RestoreTape[] }) => {
  const [open, setOpen] = useState(false);
  const handleClickOpen = () => {
    setOpen(true);
  };
  const handleClose = () => {
    setOpen(false);
  };
  const counts = useMemo(() => tapes.map((tape) => tape.files.length), [tapes]);

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

function makeRestoreCopyingParam(jobID: bigint, param: JobRestoreCopyingParam): JobDispatchRequest {
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

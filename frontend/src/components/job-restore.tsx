import { Fragment, ChangeEvent, useState, useMemo, useContext, useCallback } from "react";
import format from "format-duration";

import { Virtuoso } from "react-virtuoso";

import { styled } from "@mui/material/styles";
import Grid from "@mui/material/Grid";
import Box from "@mui/material/Box";
import Typography from "@mui/material/Typography";
import ListItemButton from "@mui/material/ListItemButton";
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
import { FileListItem } from "./job-file-list-item";

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
            <Stack direction="row" spacing={1} useFlexGap flexWrap="wrap">
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

const TapeRow = styled(ListItemButton)({
  padding: "0.2rem",
  width: "100%",
  cursor: "pointer",
});
const FileRow = styled(FileListItem)({ paddingLeft: "1rem" });

interface RowData {
  type: "tape" | "file";
  label: React.ReactNode;
  opened?: boolean;
}

const RestoreViewFilesDialog = ({ tapes }: { tapes: RestoreTape[] }) => {
  const [open, setOpen] = useState(false);
  const handleClickOpen = () => {
    setOpen(true);
  };
  const handleClose = () => {
    setOpen(false);
  };

  const [openedTapeIDs, setOpenedTapeIDs] = useState<bigint[]>([]);
  const clickTapeRow = useCallback(
    (id: bigint, opened: boolean) => {
      if (opened) {
        setOpenedTapeIDs(openedTapeIDs.filter((tapeID) => tapeID !== id));
        return;
      }

      setOpenedTapeIDs([...openedTapeIDs, id]);
      return;
    },
    [openedTapeIDs, setOpenedTapeIDs],
  );

  const rows = useMemo(() => {
    const rows: RowData[] = [];
    for (const tape of tapes) {
      const opened = openedTapeIDs.includes(tape.tapeId);
      rows.push({
        type: "tape",
        label: (
          <TapeRow onClick={() => clickTapeRow(tape.tapeId, opened)}>
            {opened ? <ExpandMoreIcon /> : <ChevronRightIcon />}
            {tape.barcode}
          </TapeRow>
        ),
        opened,
      });

      if (!opened) {
        continue;
      }

      for (const file of tape.files) {
        rows.push({
          type: "file",
          label: <FileRow src={{ path: file.tapePath, size: file.size, status: file.status }} />,
        });
      }
    }

    return rows;
  }, [tapes, openedTapeIDs]);

  return (
    <Fragment>
      <Button size="small" onClick={handleClickOpen}>
        View Files
      </Button>
      {open && (
        <Dialog open={true} onClose={handleClose} maxWidth={"lg"} fullWidth scroll="paper" sx={{ height: "100%" }} className="view-log-dialog">
          <DialogTitle>View Files</DialogTitle>
          <DialogContent dividers style={{ padding: 0 }}>
            <Virtuoso
              style={{ width: "100%", height: "100%" }}
              totalCount={rows.length}
              itemContent={(idx) => {
                const row = rows[idx];
                if (!row) {
                  return null;
                }

                return row.label;
              }}
            />
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

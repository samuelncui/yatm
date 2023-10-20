import { Fragment, ChangeEvent } from "react";
import { useState, useRef, useEffect, useCallback } from "react";
import { createContext } from "react";

import Grid from "@mui/material/Grid";
import Box from "@mui/material/Box";
import List from "@mui/material/List";
import ListItemButton from "@mui/material/ListItemButton";
import ListItemText from "@mui/material/ListItemText";
import Button from "@mui/material/Button";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogTitle from "@mui/material/DialogTitle";
import DialogContent from "@mui/material/DialogContent";
import LinearProgress from "@mui/material/LinearProgress";

import { cli, fileBase, JOB_STATUS_VISIBLE } from "../api";
import { Job, JobDisplay, JobListRequest, JobStatus, LibraryEntityType } from "../entity";

import { download } from "../tools";

import { JobCard } from "../components/job-card";
import { ArchiveCard } from "../components/job-archive";
import { RestoreCard } from "../components/job-restore";

export const JobsType = "jobs";
export const RefreshContext = createContext<() => Promise<void>>(async () => {});

type DisplayableJob = Job & Partial<JobDisplay>;

export const JobsBrowser = () => {
  const [jobs, setJobs] = useState<DisplayableJob[] | null>(null);
  const [latestUpdateTimeNs, setLatestUpdateTimeNs] = useState<bigint>(0n);

  const refresh = useCallback(
    async (refresh?: boolean) => {
      const [results, updated] = await (async () => {
        const req: JobListRequest = refresh
          ? { param: { oneofKind: "list", list: {} } }
          : { param: { oneofKind: "recentlyUpdate", recentlyUpdate: { updateSinceNs: latestUpdateTimeNs } } };

        const reply = await cli.jobList(req).response;
        if (reply.jobs.length === 0) {
          if (refresh) {
            return [[], true];
          }
          return [Array.from(jobs || []), false];
        }

        const latest = reply.jobs.reduce((latest, job) => {
          if (!job || !job.updateTimeNs) {
            return latest;
          }
          if (job.updateTimeNs > latest) {
            return job.updateTimeNs;
          }
          return latest;
        }, 0n);
        console.log("refresh jobs list, set latest update, latest=", latest);
        setLatestUpdateTimeNs(latest);

        const results = Array.from(jobs || []);
        for (const job of reply.jobs) {
          const foundIdx = results.findIndex((target) => target.id === job.id);
          if (foundIdx >= 0) {
            results[foundIdx] = job;
            continue;
          }

          results.push(job);
        }
        return [results.filter((job) => job && job.status < JOB_STATUS_VISIBLE).sort((a, b) => Number(b.createTimeNs - a.createTimeNs)), true];
      })();

      const displays = new Map<BigInt, JobDisplay>();
      const processingJobs = results.filter((job) => job.status === JobStatus.PROCESSING);
      if (processingJobs.length === 0) {
        if (updated) {
          setJobs(results);
        }
        return;
      }

      for (const reply of await Promise.all(
        processingJobs.map((job) => cli.jobDisplay({ id: job.id }).response.then((reply) => ({ ...reply, jobID: job.id }))),
      )) {
        if (!reply.display) {
          continue;
        }
        displays.set(reply.jobID, reply.display);
      }

      setJobs(results.map((job) => ({ ...job, ...displays.get(job.id) })));
    },
    [jobs, setJobs, latestUpdateTimeNs, setLatestUpdateTimeNs],
  );
  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;

  useEffect(() => {
    refreshRef.current(true);
    const timer = setInterval(() => refreshRef.current(), 2000);

    return () => {
      if (!timer) {
        return;
      }

      clearInterval(timer);
    };
  }, []);
  useEffect(() => console.log("jobs changed,", jobs), [jobs]);

  return (
    <RefreshContext.Provider value={refresh}>
      <Box className="browser-box">
        <Grid className="browser-container" container>
          <Grid className="browser" item xs={12} md={2} sx={{ display: { xs: "none", md: "block" } }}>
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
              <ListItemButton
                onClick={async () => {
                  const resp = await cli.libraryExport({ types: [LibraryEntityType.FILE, LibraryEntityType.TAPE, LibraryEntityType.POSITION] }).response;
                  download(resp.json, "library.json", "application/json");
                }}
              >
                <ListItemText primary="Export Library" />
              </ListItemButton>
              <ImportDatabaseDialog />
            </List>
          </Grid>
          <Grid className="browser" item xs={12} md={10}>
            <div className="job-list">{jobs ? jobs.map((job) => <GetJobCard job={job} key={job.id.toString()} />) : <LinearProgress />}</div>
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
        <ListItemText primary="Import Library" />
      </ListItemButton>
      {open && (
        <Dialog open={true} onClose={handleClose} maxWidth={"sm"} fullWidth>
          <DialogTitle>Upload Library JSON</DialogTitle>
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

const GetJobCard = ({ job }: { job: DisplayableJob }): JSX.Element => {
  if (!job.state) {
    return <JobCard job={job} />;
  }

  const type = job.state?.state.oneofKind;
  switch (type) {
    case "archive":
      return <ArchiveCard job={job} state={job.state.state.archive} display={job.display?.oneofKind === "archive" ? job.display.archive : null} />;
    case "restore":
      return <RestoreCard job={job} state={job.state.state.restore} display={job.display?.oneofKind === "restore" ? job.display.restore : null} />;
    default:
      return <JobCard job={job} />;
  }
};

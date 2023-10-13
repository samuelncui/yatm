import { useCallback, useContext } from "react";

import { styled } from "@mui/material/styles";
import Typography from "@mui/material/Typography";
import Card from "@mui/material/Card";
import CardActions from "@mui/material/CardActions";
import CardContent from "@mui/material/CardContent";
import Button from "@mui/material/Button";
import Divider from "@mui/material/Divider";

import { cli } from "../api";
import { Job, JobStatus, JobDeleteRequest } from "../entity";

import { ViewLogDialog } from "./job-log";
import { RefreshContext } from "../pages/jobs";

const DeleteJobButton = ({ jobID }: { jobID: bigint }) => {
  const refresh = useContext(RefreshContext);
  const deleteJob = useCallback(async () => {
    await cli.jobDelete(JobDeleteRequest.create({ ids: [jobID] }));
    await refresh();
  }, [jobID]);

  return (
    <Button size="small" onClick={deleteJob}>
      Delete Job
    </Button>
  );
};

const RightButtonsContainer = styled("div")({ marginLeft: "auto !important", marginRight: 0 });

export const JobCard = ({ job, detail, buttons }: { job: Job; detail?: JSX.Element; buttons?: JSX.Element }) => {
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
        <div>{buttons}</div>
        <RightButtonsContainer>
          <ViewLogDialog key="VIEW_LOG" jobID={job.id} />
          <DeleteJobButton key="DELETE_JOB" jobID={job.id} />
        </RightButtonsContainer>
      </CardActions>
    </Card>
  );
};

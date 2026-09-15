import { useCallback, useEffect, useRef, useState } from "react";
import { Alert, Button, Table, TableHead, TableBody, TableRow, TableCell } from "@mui/material";
import { Link } from "react-router";
import { jobCli } from "@/api";
import type { Job } from "@/entity";
import { jobLabel, jobStatusLabel } from "@/components/job-card";
import { contentTime } from "@/components/content-status";
import { errorMessage } from "@/tools";

export const RelatedJobs = ({ locationID, mediaID }: { locationID?: bigint; mediaID?: bigint }) => {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const request = useRef(0);
  const query = locationID ? `location=${locationID}` : `media=${mediaID}`;
  const load = useCallback(async () => {
    const sequence = ++request.current;
    setBusy(true);
    try {
      const reply = await jobCli.list({ filter: { locationId: locationID, mediaId: mediaID, limit: 10n } }).response;
      if (sequence !== request.current) return;
      setJobs(reply.jobs);
      setError("");
    } catch (error) {
      if (sequence === request.current) setError(errorMessage(error, "Could not load related Jobs"));
    } finally {
      if (sequence === request.current) setBusy(false);
    }
  }, [locationID, mediaID]);
  useEffect(() => {
    const pending = request;
    setJobs([]);
    setError("");
    void load();
    return () => {
      pending.current++;
    };
  }, [load]);
  return (
    <section className="related-jobs-panel">
      <div className="section-heading">
        <h2>Recent Jobs</h2>
        <div>
          <Button disabled={busy} onClick={() => void load()}>
            Refresh
          </Button>
          <Button component={Link} to={`/jobs?${query}`}>
            All related Jobs
          </Button>
        </div>
      </div>
      {error && <Alert severity="error">{error}</Alert>}
      <Table size="small" aria-label="Related Jobs">
        <TableHead>
          <TableRow>
            <TableCell>Job</TableCell>
            <TableCell>Status</TableCell>
            <TableCell>Created</TableCell>
            <TableCell>Updated</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {jobs.map((job) => (
            <TableRow key={String(job.id)} hover>
              <TableCell>
                <Link to={`/jobs/${job.id}`} state={{ returnTo: locationID ? `/settings/locations/${locationID}?tab=jobs` : `/jobs?${query}` }}>
                  #{String(job.id)} · {jobLabel(job.kind)}
                </Link>
              </TableCell>
              <TableCell>{jobStatusLabel(job)}</TableCell>
              <TableCell>{contentTime(job.createdAtMs)}</TableCell>
              <TableCell>{contentTime(job.updatedAtMs)}</TableCell>
            </TableRow>
          ))}
          {!jobs.length && (
            <TableRow>
              <TableCell colSpan={4}>{busy ? "Loading…" : error ? "Jobs unavailable" : "No Jobs yet"}</TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
    </section>
  );
};

import { Feedback } from "@/components/feedback";
import { useCallback, useEffect, useRef, useState } from "react";
import { Link, Route, Routes, useNavigate, useParams, useSearchParams } from "react-router";
import { Alert, Box, Button, Card, Chip, Stack, Tab, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, Typography } from "@mui/material";
import { locationCli, jobCli } from "@/api";
import { ActionRow } from "@/components/action-row";
import { SettingsPage } from "@/components/settings-page";
import { PageHeading } from "@/components/page-heading";
import { PageTabs } from "@/components/page-navigation";
import { Location, GetLocationResponse, type Job } from "@/entity";
import { contentTime } from "@/components/content-status";
import { LocationJobs } from "@/components/location-jobs";
import { jobStatusLabel } from "@/components/job-card";
import { LocationForm } from "@/components/location-form";
import { errorMessage } from "@/tools";
import { LocationEntryBrowser } from "@/components/location-entry-browser";
import { useActionDialog } from "@/components/action-dialog";

export const LocationsBrowser = () => (
  <Routes>
    <Route index element={<LocationList />} />
    <Route path="new" element={<NewLocation />} />
    <Route path=":id" element={<LocationDetail />} />
  </Routes>
);

const LocationList = () => {
  const [locations, setLocations] = useState<Location[]>([]);
  const [observations, setObservations] = useState<Record<string, GetLocationResponse>>({});
  const [jobs, setJobs] = useState<Record<string, Job>>({});
  const [detailErrors, setDetailErrors] = useState<Record<string, { access?: string; job?: string }>>({});
  const [more, setMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const request = useRef(0);
  const load = useCallback(async (afterId = 0n) => {
    const sequence = ++request.current;
    setLoading(true);
    try {
      const page = await locationCli.list({ afterId, limit: 20, query: "" }).response;
      if (sequence !== request.current) return;
      setLocations((current) => (afterId ? [...current, ...page.locations] : page.locations));
      setMore(page.hasMore);
      setError("");
      const jobLocations = page.locations.filter((location) => location.lastJobId > 0n);
      const [checks, recent] = await Promise.all([
        Promise.allSettled(page.locations.map((location) => locationCli.get({ id: location.id }).response)),
        Promise.allSettled(jobLocations.map((location) => jobCli.get({ id: location.lastJobId }).response)),
      ]);
      if (sequence !== request.current) return;
      const observations: Record<string, GetLocationResponse> = {};
      const jobs: Record<string, Job> = {};
      const errors: Record<string, { access?: string; job?: string }> = {};
      checks.forEach((result, index) => {
        const id = String(page.locations[index].id);
        errors[id] = {};
        if (result.status === "rejected") {
          errors[id].access = errorMessage(result.reason, "Could not check access");
          return;
        }
        observations[id] = result.value;
      });
      recent.forEach((result, index) => {
        const location = jobLocations[index];
        if (result.status === "rejected") {
          errors[String(location.id)].job = errorMessage(result.reason, "Could not load Job details");
          return;
        }
        if (!result.value.job) {
          errors[String(location.id)].job = "Job no longer exists";
          return;
        }
        jobs[String(result.value.job.id)] = result.value.job;
      });
      setObservations((current) => ({ ...current, ...observations }));
      setJobs((current) => ({ ...current, ...jobs }));
      setDetailErrors((current) => ({ ...current, ...errors }));
    } catch (error) {
      if (sequence === request.current) setError(errorMessage(error, "Could not load locations"));
    } finally {
      if (sequence === request.current) setLoading(false);
    }
  }, []);
  useEffect(() => {
    const pending = request;
    void load();
    return () => {
      pending.current++;
    };
  }, [load]);
  return (
    <SettingsPage title="Locations" description="Directories YATM may read originals from and write Restore output to.">
      {error && <Feedback severity="error">{error}</Feedback>}
      <Card>
        <TableContainer>
          <Table aria-label="Locations" size="small">
            <TableHead>
              <TableRow>
                <TableCell>Name / directory</TableCell>
                <TableCell>Restore preference</TableCell>
                <TableCell>Access</TableCell>
                <TableCell>Latest analysis</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {locations.map((location) => {
                const observation = observations[String(location.id)]?.accessibility;
                const detailsError = detailErrors[String(location.id)];
                return (
                  <TableRow key={String(location.id)} hover>
                    <TableCell>
                      <Link to={String(location.id)}>{location.name}</Link>
                      <Typography variant="caption" color="text.secondary" component="div">
                        <code>{location.rootPath}</code>
                      </Typography>
                    </TableCell>
                    <TableCell>{location.restoreTarget ? "Preferred" : "—"}</TableCell>
                    <TableCell>
                      <span title={detailsError?.access || observation?.error || (observation ? contentTime(observation.checkedAtNs || undefined) : "")}>
                        {!observation ? (detailsError?.access ? "Check failed" : "Not checked") : observation.accessible ? "Available" : "Unavailable"}
                      </span>
                      {observation && detailsError?.access && (
                        <Typography variant="caption" color="text.secondary" component="div" title={detailsError.access}>
                          Last check · Refresh failed
                        </Typography>
                      )}
                    </TableCell>
                    <TableCell>
                      {location.lastJobId ? (
                        <>
                          <Link to={`/jobs/${location.lastJobId}`}>
                            {jobs[String(location.lastJobId)] ? jobStatusLabel(jobs[String(location.lastJobId)]) : "View job"}
                          </Link>
                          <Typography variant="caption" color="text.secondary" component="div">
                            {contentTime(jobs[String(location.lastJobId)]?.createdAtNs || undefined, "")}
                          </Typography>
                          {detailsError?.job && (
                            <Typography variant="caption" color="text.secondary" component="div" title={detailsError.job}>
                              Details unavailable
                            </Typography>
                          )}
                        </>
                      ) : (
                        "—"
                      )}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </TableContainer>
      </Card>
      {!loading && !locations.length && (
        <Typography variant="body2" color="text.secondary">
          Add an original directory or a restore destination.
        </Typography>
      )}
      <ActionRow>
        <Button component={Link} to="new" variant="contained">
          Add location
        </Button>
        <Button disabled={loading} onClick={() => void load()}>
          Refresh
        </Button>
        {more && (
          <Button disabled={loading} onClick={() => void load(locations.at(-1)?.id)}>
            Load more
          </Button>
        )}
      </ActionRow>
    </SettingsPage>
  );
};

const NewLocation = () => {
  const navigate = useNavigate();
  return (
    <SettingsPage title="Add location" description="Register a directory YATM may read originals from.">
      <LocationForm
        onCancel={() => navigate("/settings/locations")}
        onSaved={(location) => navigate(`/settings/locations/${location.id}`, { replace: true })}
      />
    </SettingsPage>
  );
};

const LocationDetail = () => {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const tab = params.get("tab") ?? "configuration";
  const [reply, setReply] = useState<GetLocationResponse>();
  const [error, setError] = useState("");
  const [dirty, setDirty] = useState(false);
  const { ask: askAction, dialog: actionDialog } = useActionDialog();
  const request = useRef(0);
  const load = useCallback(async () => {
    const sequence = ++request.current;
    try {
      if (!/^[1-9]\d*$/.test(id)) throw new Error("Invalid Location");
      const next = await locationCli.get({ id: BigInt(id) }).response;
      if (sequence !== request.current) return;
      setReply(next);
      setError("");
    } catch (error) {
      if (sequence === request.current) setError(errorMessage(error, "Could not load location"));
    }
  }, [id]);
  useEffect(() => {
    const pending = request;
    setReply(undefined);
    setDirty(false);
    setError("");
    void load();
    return () => {
      pending.current++;
    };
  }, [load]);
  const source = reply?.location;
  if (!source) return error ? <Feedback>{error}</Feedback> : <Alert severity="info">Loading location…</Alert>;
  return (
    <Stack useFlexGap spacing={2} sx={{ width: "100%", height: "100%", minHeight: 0, p: 3, textAlign: "left" }}>
      <PageHeading
        title={source.name}
        actions={
          <Chip size="small" label={!reply?.accessibility ? "Not checked" : reply.accessibility.accessible ? "Available" : "Unavailable"} variant="outlined" />
        }
      />
      {error && <Feedback severity="error">{error}</Feedback>}
      {reply?.warnings.map((warning) => (
        <Alert severity="warning" key={warning}>
          {warning}
        </Alert>
      ))}
      <PageTabs value={tab} onChange={(_, value) => setParams({ tab: value })} aria-label="Location details">
        <Tab value="configuration" label="Configuration" />
        <Tab value="files" label="Files" />
        <Tab value="jobs" label="Jobs" />
      </PageTabs>
      <Box sx={{ flex: 1, minHeight: 0, overflowY: tab === "files" ? "hidden" : "auto" }}>
        {tab === "configuration" && (
          <Stack useFlexGap spacing={2}>
            <LocationForm
              key={String(source.revision)}
              source={source}
              onDirtyChange={setDirty}
              onCancel={() => navigate("/settings/locations")}
              onSaved={(location) => {
                setReply(GetLocationResponse.create({ location }));
                void load();
              }}
              additionalActions={(busy) => ({
                beforeCancel: (
                  <Button component={Link} to={`/scan?location=${source.id}`} disabled={busy}>
                    Scan
                  </Button>
                ),
                afterCancel: (
                  <Button
                    color="error"
                    disabled={dirty || busy}
                    onClick={() =>
                      askAction({
                        title: `Remove ${source.name}?`,
                        confirmLabel: "Remove",
                        danger: true,
                        children: "Removes the registration and original links. Disk files, Library organization, saved versions and archive copies are kept.",
                        onConfirm: async () => {
                          await locationCli.delete({ dryrun: false, id: source.id }).response;
                          navigate("/settings/locations");
                        },
                      })
                    }
                  >
                    Remove location
                  </Button>
                ),
              })}
            />
          </Stack>
        )}
        {tab === "files" && (
          <Box sx={{ height: "100%", display: "flex", flexDirection: "column" }}>
            <LocationEntryBrowser source={source} fill />
          </Box>
        )}
        {tab === "jobs" && <LocationJobs locationID={source.id} />}
      </Box>
      {actionDialog}
    </Stack>
  );
};

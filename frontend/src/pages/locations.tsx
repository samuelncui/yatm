import { Feedback } from "@/components/feedback";
import { useCallback, useEffect, useRef, useState } from "react";
import { Link, Route, Routes, useNavigate, useParams, useSearchParams } from "react-router";
import { Alert, Box, Button, Card, Chip, Stack, Tab, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, Typography } from "@mui/material";
import { locationCli, jobCli } from "@/api";
import { ActionRow } from "@/components/action-row";
import { SettingsLoading, SettingsPage } from "@/components/settings-page";
import { PageHeading } from "@/components/page-heading";
import { PageTabs } from "@/components/page-navigation";
import { GetLocationResponse, type Job, type Location } from "@/entity";
import { contentTime } from "@/components/content-status";
import { LocationJobs } from "@/components/location-jobs";
import { jobStatusLabel } from "@/components/job-card";
import { LocationForm } from "@/components/location-form";
import { errorMessage } from "@/tools";
import { LocationEntryBrowser } from "@/components/location-entry-browser";
import { useActionDialog } from "@/components/action-dialog";
import { useLocationChoices } from "@/components/use-location-choices";
import { useAppStore } from "@/state/react";
import { removeLocation, saveLocation } from "@/state/locations";

export const LocationsBrowser = () => (
  <Routes>
    <Route index element={<LocationList />} />
    <Route path="new" element={<NewLocation />} />
    <Route path=":id" element={<LocationDetail />} />
  </Routes>
);

const LocationList = () => {
  const { locations, loading: catalogLoading, error, refresh } = useLocationChoices({ enabled: true });
  const [observations, setObservations] = useState<Record<string, GetLocationResponse>>({});
  const [jobs, setJobs] = useState<Record<string, Job>>({});
  const [detailErrors, setDetailErrors] = useState<Record<string, { access?: string; job?: string }>>({});
  const [checking, setChecking] = useState(false);
  const loading = catalogLoading || checking;
  const request = useRef(0);
  useEffect(() => {
    const pending = request;
    const sequence = ++pending.current;
    const check = async () => {
      setChecking(true);
      for (let offset = 0; offset < locations.length; offset += 20) {
        const page = locations.slice(offset, offset + 20);
        const checks = await Promise.allSettled(page.map((location) => locationCli.get({ id: location.id }).response));
        if (sequence !== request.current) return;
        const jobLocations: Location[] = [];
        const observations: Record<string, GetLocationResponse> = {};
        const jobs: Record<string, Job> = {};
        const errors: Record<string, { access?: string; job?: string }> = {};
        checks.forEach((result, index) => {
          const id = String(page[index].id);
          errors[id] = {};
          if (result.status === "rejected") {
            errors[id].access = errorMessage(result.reason, "Could not check access");
            errors[id].job = "Could not load latest analysis";
            return;
          }
          observations[id] = result.value;
          if (result.value.location?.lastJobId) jobLocations.push(result.value.location);
        });
        const recent = await Promise.allSettled(jobLocations.map((location) => jobCli.get({ id: location.lastJobId }).response));
        if (sequence !== request.current) return;
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
      }
      setChecking(false);
    };
    void check();
    return () => {
      pending.current++;
    };
  }, [locations]);
  return (
    <SettingsPage title="Locations" description="Directories YATM may read originals from and write Restore output to.">
      <SettingsLoading label="Location" loading={catalogLoading} />
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
                const observed = observations[String(location.id)];
                const observation = observed?.accessibility;
                const lastJobId = observed?.location?.lastJobId ?? location.lastJobId;
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
                      {lastJobId ? (
                        <>
                          <Link to={`/jobs/${lastJobId}`}>{jobs[String(lastJobId)] ? jobStatusLabel(jobs[String(lastJobId)]) : "View job"}</Link>
                          <Typography variant="caption" color="text.secondary" component="div">
                            {contentTime(jobs[String(lastJobId)]?.createdAtNs || undefined, "")}
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
      {!loading && !error && !locations.length && (
        <Typography variant="body2" color="text.secondary">
          Add an original directory or a restore destination.
        </Typography>
      )}
      <ActionRow>
        <Button component={Link} to="new" variant="contained">
          Add location
        </Button>
        <Button disabled={loading} onClick={refresh}>
          Refresh
        </Button>
      </ActionRow>
    </SettingsPage>
  );
};

const NewLocation = () => {
  const store = useAppStore();
  const navigate = useNavigate();
  return (
    <SettingsPage title="Add location" description="Register a directory YATM may read originals from.">
      <LocationForm
        onCancel={() => navigate("/settings/locations")}
        onSaved={(location) => {
          saveLocation(store, location);
          navigate(`/settings/locations/${location.id}`, { replace: true });
        }}
      />
    </SettingsPage>
  );
};

const LocationDetail = () => {
  const store = useAppStore();
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
    setError("");
    try {
      if (!/^[1-9]\d*$/.test(id)) throw new Error("Invalid Location");
      const next = await locationCli.get({ id: BigInt(id) }).response;
      if (sequence !== request.current) return;
      if (!next.location) throw new Error("Location reply is missing");
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
  const failure = error && <Feedback action={<Button onClick={() => void load()}>Retry</Button>}>{error}</Feedback>;
  if (!source) return failure || <Alert severity="info">Loading location…</Alert>;
  return (
    <Stack useFlexGap spacing={2} sx={{ width: "100%", height: "100%", minHeight: 0, p: 3, textAlign: "left" }}>
      <PageHeading
        title={source.name}
        actions={
          <Chip size="small" label={!reply?.accessibility ? "Not checked" : reply.accessibility.accessible ? "Available" : "Unavailable"} variant="outlined" />
        }
      />
      {failure}
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
                saveLocation(store, location);
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
                          removeLocation(store, source.id);
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

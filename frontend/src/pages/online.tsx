import { useCallback, useEffect, useRef, useState } from "react";
import { Link, Route, Routes, useNavigate, useParams, useSearchParams } from "react-router";
import {
  Alert,
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Tab,
  Tabs,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
} from "@mui/material";
import { locationCli, settingsCli, jobCli } from "@/api";
import { OnlineBinding, Location, LocationReply, type Job } from "@/entity";
import { contentTime, locationSetupLabel } from "@/components/content-status";
import { LocationJobs } from "@/components/location-jobs";
import { jobStatusLabel } from "@/components/job-card";
import { LocationForm } from "@/components/location-form";
import { errorMessage, runUIAction } from "@/tools";
import { LocationEntryBrowser } from "@/components/location-entry-browser";

export const LocationsBrowser = () => (
  <Routes>
    <Route index element={<LocationList />} />
    <Route path="new" element={<NewLocation />} />
    <Route path=":id" element={<LocationDetail />} />
  </Routes>
);

const LocationList = () => {
  const [locations, setLocations] = useState<Location[]>([]);
  const [observations, setObservations] = useState<Record<string, LocationReply>>({});
  const [jobs, setJobs] = useState<Record<string, Job>>({});
  const [detailErrors, setDetailErrors] = useState<Record<string, { access?: string; job?: string }>>({});
  const [messages, setMessages] = useState<string[]>([]);
  const [settingsError, setSettingsError] = useState("");
  const [more, setMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const request = useRef(0);
  const settingsRequest = useRef(0);
  const loadSettings = useCallback(async () => {
    const sequence = ++settingsRequest.current;
    try {
      const reply = await settingsCli.getAccess({}).response;
      if (sequence !== settingsRequest.current) return;
      setMessages(reply.migrationMessages);
      setSettingsError("");
    } catch (error) {
      if (sequence === settingsRequest.current) setSettingsError(errorMessage(error, "Could not load configuration details"));
    }
  }, []);
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
        Promise.allSettled(page.locations.map((location) => locationCli.get({ id: location.id, revision: 0n }).response)),
        Promise.allSettled(jobLocations.map((location) => jobCli.get({ id: location.lastJobId }).response)),
      ]);
      if (sequence !== request.current) return;
      const observations: Record<string, LocationReply> = {};
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
    const pendingSettings = settingsRequest;
    void load();
    void loadSettings();
    return () => {
      pending.current++;
      pendingSettings.current++;
    };
  }, [load, loadSettings]);
  return (
    <Box className="browser-box locations-page">
      <header className="product-page-heading">
        <h1>Locations</h1>
        <div className="product-actions">
          <Button disabled={loading} onClick={() => void load()}>
            Refresh
          </Button>
          <Button component={Link} to="new" variant="contained">
            Add location
          </Button>
        </div>
      </header>
      {error && <Alert severity="error">{error}</Alert>}
      {settingsError && (
        <Alert severity="warning">
          Configuration details unavailable: {settingsError}
          <Button onClick={() => void loadSettings()}>Retry</Button>
        </Alert>
      )}
      {messages.length > 0 && (
        <details className="migration-summary">
          <summary>Configuration migration</summary>
          {messages.map((message) => (
            <p key={message}>{message}</p>
          ))}
        </details>
      )}
      <div className="location-table">
        <Table aria-label="Locations" size="small">
          <TableHead>
            <TableRow>
              <TableCell>Name / directory</TableCell>
              <TableCell>Restore preference</TableCell>
              <TableCell>Access</TableCell>
              <TableCell>Path</TableCell>
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
                    <div className="product-muted">
                      <code>{location.rootPath}</code>
                    </div>
                  </TableCell>
                  <TableCell>{location.restoreTarget ? "Preferred" : "—"}</TableCell>
                  <TableCell>
                    <span title={detailsError?.access || observation?.error || (observation ? contentTime(observation.checkedAtMs) : "")}>
                      {!observation ? (detailsError?.access ? "Check failed" : "Not checked") : observation.accessible ? "Available" : "Unavailable"}
                    </span>
                    {observation && detailsError?.access && (
                      <div className="product-muted" title={detailsError.access}>
                        Last check · Refresh failed
                      </div>
                    )}
                  </TableCell>
                  <TableCell>{locationSetupLabel(location)}</TableCell>
                  <TableCell>
                    {location.lastJobId ? (
                      <>
                        <Link to={`/jobs/${location.lastJobId}`}>
                          {jobs[String(location.lastJobId)] ? jobStatusLabel(jobs[String(location.lastJobId)]) : "View job"}
                        </Link>
                        <div className="product-muted">{contentTime(jobs[String(location.lastJobId)]?.createdAtMs, "")}</div>
                        {detailsError?.job && (
                          <div className="product-muted" title={detailsError.job}>
                            Details unavailable
                          </div>
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
      </div>
      {!loading && !locations.length && <p className="product-muted">Add an original directory or a restore destination.</p>}
      {more && (
        <Button disabled={loading} onClick={() => void load(locations.at(-1)?.id)}>
          Load more
        </Button>
      )}
    </Box>
  );
};

const NewLocation = () => {
  const navigate = useNavigate();
  return (
    <div className="browser-box locations-page">
      <div className="settings-form-page">
        <header className="product-page-heading">
          <h1>Add location</h1>
        </header>
        <LocationForm
          onCancel={() => navigate("/settings/locations")}
          onSaved={(location) => navigate(`/settings/locations/${location.id}`, { replace: true })}
        />
      </div>
    </div>
  );
};

const LocationDetail = () => {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const tab = params.get("tab") ?? "configuration";
  const [reply, setReply] = useState<LocationReply>();
  const [error, setError] = useState("");
  const [removing, setRemoving] = useState(false);
  const [busy, setBusy] = useState(false);
  const [dirty, setDirty] = useState(false);
  const request = useRef(0);
  const load = useCallback(async () => {
    const sequence = ++request.current;
    try {
      if (!/^[1-9]\d*$/.test(id)) throw new Error("Invalid Location");
      const next = await locationCli.get({ id: BigInt(id), revision: 0n }).response;
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
  if (!source) return <Alert severity={error ? "error" : "info"}>{error || "Loading location…"}</Alert>;
  const mutate = async (action: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await action();
      await load();
    } catch (error) {
      setError(errorMessage(error, "Location operation failed"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Box className="browser-box locations-page">
      <header className="product-page-heading">
        <div>
          <Button component={Link} to="/settings/locations">
            Locations
          </Button>
          <h1>{source.name}</h1>
          <code>{source.rootPath}</code>
        </div>
        <div className="product-actions">
          <Chip size="small" label={!reply?.accessibility ? "Not checked" : reply.accessibility.accessible ? "Available" : "Unavailable"} variant="outlined" />
          {source.binding !== OnlineBinding.UNCONFIRMED && (
            <Button component={Link} to={`/scan?location=${source.id}`} variant="contained">
              Scan
            </Button>
          )}
        </div>
      </header>
      {error && <Alert severity="error">{error}</Alert>}
      {source.binding === OnlineBinding.UNCONFIRMED && (
        <Alert
          severity="warning"
          action={
            <Button disabled={busy || dirty} onClick={() => void mutate(() => locationCli.confirm({ id: source.id, revision: source.revision }).response)}>
              Confirm this path
            </Button>
          }
        >
          Imported directory. Confirm its path on this installation before use.
        </Alert>
      )}
      {reply?.warnings.map((warning) => (
        <Alert severity="warning" key={warning}>
          {warning}
        </Alert>
      ))}
      <Tabs value={tab} onChange={(_, value) => setParams({ tab: value })} aria-label="Location details">
        <Tab value="configuration" label="Configuration" />
        <Tab value="files" label="Files" />
        <Tab value="jobs" label="Jobs" />
      </Tabs>
      {tab === "configuration" && (
        <div className="settings-form-page">
          <LocationForm
            key={String(source.revision)}
            source={source}
            required={reply?.requiredExclusions}
            onDirtyChange={setDirty}
            onCancel={() => navigate("/settings/locations")}
            onSaved={(location) => {
              setReply(LocationReply.create({ location }));
              void load();
            }}
          />
          <section className="location-remove">
            <Button color="error" disabled={dirty || busy} onClick={() => setRemoving(true)}>
              Remove location
            </Button>
          </section>
        </div>
      )}
      {tab === "files" && (
        <section className="location-files-page">
          <LocationEntryBrowser source={source} />
        </section>
      )}
      {tab === "jobs" && <LocationJobs locationID={source.id} />}
      <Dialog open={removing} onClose={() => !busy && setRemoving(false)}>
        <DialogTitle>Remove {source.name}?</DialogTitle>
        <DialogContent>
          Removes the registration and original links. Disk files, Library organization, saved versions and archive copies are kept.
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setRemoving(false)}>Cancel</Button>
          <Button
            color="error"
            disabled={busy}
            onClick={() =>
              runUIAction(async () => {
                setBusy(true);
                try {
                  await locationCli.delete({ id: source.id, revision: source.revision }).response;
                  navigate("/settings/locations");
                } finally {
                  setBusy(false);
                }
              }, "Could not remove location")
            }
          >
            Remove
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
};

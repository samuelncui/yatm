import { useState } from "react";
import { Alert, Button, MenuItem, TextField } from "@mui/material";
import { useSearchParams } from "react-router";
import { cli, locationCli } from "@/api";
import { JobKind, JobStatus, type JobFilter, type Location, type Media } from "@/entity";
import { jobLabel } from "@/components/job-card";
import { errorMessage } from "@/tools";

export const jobKinds = [JobKind.ARCHIVE, JobKind.RESTORE, JobKind.SCAN];
const statuses = [
  { value: JobStatus.INDEXING, label: "Preparing" },
  { value: JobStatus.PENDING, label: "Pending" },
  { value: JobStatus.COMPLETED, label: "Completed" },
];
export function jobFilterFromParams(params: URLSearchParams): JobFilter {
  const kind = Number(params.get("kind"));
  const status = Number(params.get("status"));
  const location = params.get("location");
  const media = params.get("media");
  return {
    ...(params.has("kind") && jobKinds.includes(kind) ? { kind } : {}),
    ...(params.has("status") && statuses.some((item) => item.value === status) ? { status } : {}),
    ...(location && /^[1-9]\d*$/.test(location) ? { locationId: BigInt(location) } : {}),
    ...(media && /^[1-9]\d*$/.test(media) ? { mediaId: BigInt(media) } : {}),
  };
}
export const JobFilters = () => {
  const [params, setParams] = useSearchParams();
  const filter = jobFilterFromParams(params);
  const [locations, setLocations] = useState<Location[]>([]);
  const [media, setMedia] = useState<Media[]>([]);
  const [moreLocations, setMoreLocations] = useState(false);
  const [moreMedia, setMoreMedia] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const loadResources = async (more = false) => {
    if (loading) return;
    setLoading(true);
    try {
      const [locationPage, mediaPage] = await Promise.all([
        !more || moreLocations ? locationCli.list({ limit: 50, afterId: more ? (locations.at(-1)?.id ?? 0n) : 0n, query: "" }).response : undefined,
        !more || moreMedia
          ? cli.mediaList({ param: { oneofKind: "list", list: { kinds: [], limit: 50n, offset: more ? BigInt(media.length) : 0n, query: "" } } }).response
          : undefined,
      ]);
      if (locationPage) {
        setLocations((current) => (more ? [...current, ...locationPage.locations] : locationPage.locations));
        setMoreLocations(locationPage.hasMore);
      }
      if (mediaPage) {
        setMedia((current) => (more ? [...current, ...mediaPage.media] : mediaPage.media));
        setMoreMedia(mediaPage.hasMore);
      }
      setLoaded(true);
      setError("");
    } catch (error) {
      setError(errorMessage(error, "Could not list resources"));
    } finally {
      setLoading(false);
    }
  };
  const change = (key: string, value: string) => {
    const next = new URLSearchParams(params);
    next.delete(key);
    if (value) next.set(key, value);
    setParams(next);
  };
  const resource = filter.locationId ? `location:${filter.locationId}` : filter.mediaId ? `media:${filter.mediaId}` : "";
  const knownResource = locations.some((value) => resource === `location:${value.id}`) || media.some((value) => resource === `media:${value.id}`);
  return (
    <div className="job-filters">
      <TextField
        className="job-filter"
        select
        size="small"
        label="Job type"
        slotProps={{ inputLabel: { shrink: true }, select: { displayEmpty: true } }}
        value={filter.kind ?? ""}
        onChange={(event) => change("kind", event.target.value)}
      >
        <MenuItem value="">All types</MenuItem>
        {jobKinds.map((kind) => (
          <MenuItem key={kind} value={kind}>
            {jobLabel(kind)}
          </MenuItem>
        ))}
      </TextField>
      <TextField
        className="job-filter"
        select
        size="small"
        label="Status"
        slotProps={{ inputLabel: { shrink: true }, select: { displayEmpty: true } }}
        value={filter.status ?? ""}
        onChange={(event) => change("status", event.target.value)}
      >
        <MenuItem value="">All statuses</MenuItem>
        {statuses.map((item) => (
          <MenuItem key={item.value} value={item.value}>
            {item.label}
          </MenuItem>
        ))}
      </TextField>
      <TextField
        className="job-filter job-filter-resource"
        select
        size="small"
        label="Resource"
        value={resource}
        slotProps={{
          inputLabel: { shrink: true },
          select: {
            displayEmpty: true,
            onOpen: () => {
              if (!loaded) void loadResources();
            },
          },
        }}
        onChange={(event) => {
          if (event.target.value === "more") {
            void loadResources(true);
            return;
          }
          const next = new URLSearchParams(params);
          next.delete("location");
          next.delete("media");
          const [kind, id] = event.target.value.split(":");
          if (id) next.set(kind, id);
          setParams(next);
        }}
      >
        <MenuItem value="">All resources</MenuItem>
        {resource && !knownResource && <MenuItem value={resource}>{filter.locationId ? `Location ${filter.locationId}` : `Media ${filter.mediaId}`}</MenuItem>}
        {locations.map((value) => (
          <MenuItem key={`location:${value.id}`} value={`location:${value.id}`}>
            Location · {value.name}
          </MenuItem>
        ))}
        {media.map((value) => (
          <MenuItem key={`media:${value.id}`} value={`media:${value.id}`}>
            Media · {value.name}
          </MenuItem>
        ))}
        {(moreLocations || moreMedia) && (
          <MenuItem value="more" disabled={loading}>
            Load more resources…
          </MenuItem>
        )}
      </TextField>
      {(filter.kind !== undefined || filter.status !== undefined || resource) && <Button onClick={() => setParams({})}>Clear</Button>}
      {error && (
        <Alert severity="error">
          {error}
          <Button onClick={() => void loadResources()}>Retry</Button>
        </Alert>
      )}
    </div>
  );
};

import { Feedback } from "@/components/feedback";
import { useState } from "react";
import { Button, MenuItem, TextField } from "@mui/material";
import { useSearchParams } from "react-router";
import { mediaCli } from "@/api";
import { JobKind, type JobFilter, type Media } from "@/entity";
import { jobLabel } from "@/components/job-card";
import { errorMessage } from "@/tools";
import { useLocationChoices } from "./use-location-choices";

export const jobKinds = [JobKind.ARCHIVE, JobKind.RESTORE, JobKind.SCAN];
export function jobFilterFromParams(params: URLSearchParams): JobFilter {
  const kind = Number(params.get("kind"));
  const location = params.get("location");
  const media = params.get("media");
  return {
    ...(params.has("kind") && jobKinds.includes(kind) ? { kind } : {}),
    ...(location && /^[1-9]\d*$/.test(location) ? { locationId: BigInt(location) } : {}),
    ...(media && /^[1-9]\d*$/.test(media) ? { mediaId: BigInt(media) } : {}),
  };
}
export const JobFilters = () => {
  const [params, setParams] = useSearchParams();
  const filter = jobFilterFromParams(params);
  const [open, setOpen] = useState(false);
  const choices = useLocationChoices({ enabled: open });
  const { locations } = choices;
  const [media, setMedia] = useState<Media[]>([]);
  const [moreMedia, setMoreMedia] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const loadMedia = async (more = false) => {
    if (loading) return;
    setLoading(true);
    try {
      const page = await mediaCli.list({ param: { oneofKind: "list", list: { kinds: [], limit: 50n, offset: more ? BigInt(media.length) : 0n, query: "" } } })
        .response;
      setMedia((current) => (more ? [...current, ...page.media] : page.media));
      setMoreMedia(page.hasMore);
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
              setOpen(true);
              if (!loaded && !error) void loadMedia();
            },
            onClose: () => setOpen(false),
          },
        }}
        onChange={(event) => {
          if (event.target.value === "more") {
            void loadMedia(true);
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
        {(loading || choices.loading) && <MenuItem disabled>Loading resources…</MenuItem>}
        {moreMedia && (
          <MenuItem value="more" disabled={loading}>
            Load more resources…
          </MenuItem>
        )}
      </TextField>
      {(filter.kind !== undefined || resource) && <Button onClick={() => setParams({})}>Clear</Button>}
      {(error || choices.error) && (
        <Feedback
          severity="error"
          action={
            <Button
              disabled={loading || choices.loading}
              onClick={() => {
                if (choices.error) choices.retry();
                if (error) void loadMedia(loaded);
              }}
            >
              Retry
            </Button>
          }
        >
          {[choices.error, error].filter(Boolean).join(" ")}
        </Feedback>
      )}
    </div>
  );
};

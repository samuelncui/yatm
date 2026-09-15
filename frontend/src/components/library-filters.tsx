import { useCallback, useEffect, useId, useRef, useState } from "react";
import {
  Alert,
  Button,
  Checkbox,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  InputAdornment,
  IconButton,
  MenuItem,
  Stack,
  TextField,
} from "@mui/material";
import { Clear, SearchRounded, Tune } from "@mui/icons-material";
import { locationCli } from "@/api";
import type { Location } from "@/entity";
import { errorMessage } from "@/tools";

export const backupFilters = [
  { value: "all", label: "Any archived content", query: "" },
  { value: "needed", label: "No known archived copy", query: "has:online AND NOT has:unknown AND NOT has:archive" },
  { value: "saved", label: "Has an archived copy", query: "has:archive" },
  { value: "unknown", label: "Content not checked", query: "has:online AND has:unknown" },
] as const;

export const buildLibraryQuery = (text: string, advanced: boolean, location: string, status: string, duplicates = false) => {
  const parts: string[] = [];
  if (text.trim()) {
    const value = JSON.stringify(text.trim());
    parts.push(advanced ? `(${text.trim()})` : `(name:${value} OR tag:${value} OR note:${value})`);
  }
  if (/^[1-9]\d*$/.test(location)) parts.push(`location:${location}`);
  const filter = backupFilters.find((filter) => filter.value === status)?.query;
  if (filter) parts.push(`(${filter})`);
  if (duplicates) parts.push("has:duplicates");
  return parts.join(" AND ");
};

export const LibraryFilters = ({
  onSearch,
  onClear,
  initialQuery = "",
  scopeLabel = "Library",
  locationScope,
}: {
  onSearch: (query: string, grouped: boolean) => void;
  onClear: () => void;
  initialQuery?: string;
  scopeLabel?: string;
  locationScope?: { id: string; name: string };
}) => {
  const [text, setText] = useState(initialQuery);
  const [open, setOpen] = useState(false);
  const [locations, setLocations] = useState<Location[]>([]);
  const [more, setMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [location, setLocation] = useState("all");
  const [status, setStatus] = useState("all");
  const [duplicates, setDuplicates] = useState(false);
  const [grouped, setGrouped] = useState(false);
  const [tag, setTag] = useState("");
  const [note, setNote] = useState("");
  const request = useRef(0);
  const pending = useRef(false);
  const retryAfter = useRef(0n);
  const titleID = useId();
  const submit = (query: string, groupResults = grouped) => {
    const value = query.trim();
    if (!value) {
      onClear();
      return;
    }
    onSearch(value, groupResults);
  };
  const load = useCallback(async (afterId = 0n) => {
    if (pending.current) return;
    const generation = ++request.current;
    pending.current = true;
    retryAfter.current = afterId;
    setLoading(true);
    setError("");
    try {
      const reply = await locationCli.list({ afterId, limit: 50, query: "" }).response;
      if (generation !== request.current) return;
      setLocations((current) => [...new Map([...(afterId ? current : []), ...reply.locations].map((item) => [item.id, item])).values()]);
      setMore(reply.hasMore);
    } catch (error) {
      if (generation !== request.current) return;
      setError(errorMessage(error, "Could not load Locations"));
    } finally {
      if (generation === request.current) {
        pending.current = false;
        setLoading(false);
      }
    }
  }, []);
  const showLocations = open && (!locationScope || duplicates);
  useEffect(() => {
    if (!showLocations) return;
    const activeRequest = request;
    const activeLoad = pending;
    void load();
    return () => {
      activeRequest.current++;
      activeLoad.current = false;
    };
  }, [showLocations, load]);
  const apply = () => {
    const parts = [buildLibraryQuery(text, true, locationScope && !duplicates ? "all" : location, status, duplicates)];
    if (tag.trim()) parts.push(`tag:${JSON.stringify(tag.trim())}`);
    if (note.trim()) parts.push(`note:${JSON.stringify(note.trim())}`);
    const query = parts.filter(Boolean).join(" AND ");
    setText(query);
    setGrouped(duplicates);
    setOpen(false);
    setLocation("all");
    setStatus("all");
    setDuplicates(false);
    setTag("");
    setNote("");
    submit(query, duplicates);
  };
  return (
    <>
      <form
        className="files-query-bar"
        onSubmit={(event) => {
          event.preventDefault();
          submit(text);
        }}
      >
        <TextField
          size="small"
          fullWidth
          label="Search query"
          placeholder="name:*.jpg AND tag:travel"
          value={text}
          onChange={(event) => setText(event.target.value)}
          slotProps={{
            htmlInput: { maxLength: 4096 },
            input: {
              startAdornment: (
                <InputAdornment position="start">
                  <SearchRounded fontSize="small" />
                </InputAdornment>
              ),
              endAdornment: (
                <InputAdornment position="end">
                  <IconButton
                    size="small"
                    aria-label="Clear search"
                    disabled={!text}
                    onClick={() => {
                      setText("");
                      setGrouped(false);
                      onClear();
                    }}
                  >
                    <Clear fontSize="small" />
                  </IconButton>
                </InputAdornment>
              ),
            },
          }}
        />
        <Button
          startIcon={<Tune />}
          aria-haspopup="dialog"
          onClick={() => {
            setOpen(true);
            setDuplicates(grouped);
          }}
        >
          More filters
        </Button>
        <Button type="submit">Search</Button>
      </form>
      <Dialog open={open} onClose={() => setOpen(false)} aria-labelledby={titleID} fullWidth maxWidth="sm">
        <DialogTitle id={titleID}>Filter {scopeLabel}</DialogTitle>
        <DialogContent>
          <Stack spacing={2} sx={{ pt: 1 }}>
            <TextField label="Tag" value={tag} onChange={(event) => setTag(event.target.value)} />
            <TextField label="Note" value={note} onChange={(event) => setNote(event.target.value)} />
            <TextField
              select
              label="Location"
              value={locationScope && !duplicates ? "current" : location}
              disabled={!!locationScope && !duplicates}
              onChange={(event) => setLocation(event.target.value)}
            >
              {locationScope && !duplicates && <MenuItem value="current">{locationScope.name}</MenuItem>}
              <MenuItem value="all">All locations</MenuItem>
              {locations.map((item) => (
                <MenuItem key={String(item.id)} value={String(item.id)}>
                  {item.name}
                </MenuItem>
              ))}
            </TextField>
            {showLocations && (more || loading) && !error && (
              <Button disabled={loading} onClick={() => void load(locations.at(-1)?.id)}>
                {loading ? "Loading locations…" : "More locations"}
              </Button>
            )}
            <TextField select label="Archived content" value={status} onChange={(event) => setStatus(event.target.value)}>
              {backupFilters.map((item) => (
                <MenuItem key={item.value} value={item.value}>
                  {item.label}
                </MenuItem>
              ))}
            </TextField>
            <FormControlLabel control={<Checkbox checked={duplicates} onChange={(_, checked) => setDuplicates(checked)} />} label="Duplicates in Locations" />
            {showLocations && error && (
              <Alert severity="warning">
                {error}
                <Button disabled={loading} onClick={() => void load(retryAfter.current)}>
                  Retry
                </Button>
              </Alert>
            )}
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setOpen(false)}>Cancel</Button>
          <Button variant="contained" onClick={apply}>
            Apply filters
          </Button>
        </DialogActions>
      </Dialog>
    </>
  );
};

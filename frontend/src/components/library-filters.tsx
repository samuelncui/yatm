import { Feedback } from "@/components/feedback";
import { useId, useState } from "react";
import {
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
import { useNavigate } from "react-router";
import type { Location } from "@/entity";
import { useLocationChoices } from "./use-location-choices";

export const archiveFilters = [
  { value: "all", label: "Any archived content", query: "" },
  { value: "needed", label: "No known archived copy", query: "has:original AND NOT has:unknown AND NOT has:archive" },
  { value: "saved", label: "Has an archived copy", query: "has:archive" },
  { value: "unknown", label: "Content not checked", query: "has:original AND has:unknown" },
] as const;

export const buildLibraryQuery = (text: string, advanced: boolean, location: string, status: string, duplicates = false) => {
  const parts: string[] = [];
  if (text.trim()) {
    const value = JSON.stringify(text.trim());
    parts.push(advanced ? `(${text.trim()})` : `(name:${value} OR tag:${value} OR note:${value})`);
  }
  if (/^[1-9]\d*$/.test(location)) parts.push(`location:${location}`);
  const filter = archiveFilters.find((filter) => filter.value === status)?.query;
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
  searchActive = false,
}: {
  onSearch: (query: string, grouped: boolean) => void;
  onClear: () => void;
  initialQuery?: string;
  scopeLabel?: string;
  locationScope?: { id: string; name: string };
  searchActive?: boolean;
}) => {
  const navigate = useNavigate();
  const [text, setText] = useState(initialQuery);
  const [open, setOpen] = useState(false);
  const [location, setLocation] = useState<Location>();
  const [status, setStatus] = useState("all");
  const [duplicates, setDuplicates] = useState(false);
  const [tag, setTag] = useState("");
  const [note, setNote] = useState("");
  const showLocations = open && (!locationScope || duplicates);
  const { locations, more, loading, error, loadMore, retry } = useLocationChoices({ enabled: showLocations });
  const options = location && !locations.some((item) => item.id === location.id) ? [location, ...locations] : locations;
  const titleID = useId();
  const submit = (query: string) => {
    const value = query.trim();
    if (!value) {
      onClear();
      return;
    }
    onSearch(value, false);
  };
  const apply = () => {
    const parts = [buildLibraryQuery(text, true, locationScope && !duplicates ? "all" : String(location?.id ?? "all"), status, duplicates)];
    if (tag.trim()) parts.push(`tag:${JSON.stringify(tag.trim())}`);
    if (note.trim()) parts.push(`note:${JSON.stringify(note.trim())}`);
    const query = parts.filter(Boolean).join(" AND ");
    setText(query);
    setOpen(false);
    setLocation(undefined);
    setStatus("all");
    setDuplicates(false);
    setTag("");
    setNote("");
    submit(query);
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
                    disabled={!text && !searchActive}
                    onClick={() => {
                      setText("");
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
          }}
        >
          More filters
        </Button>
        <Button type="submit">Search</Button>
        <Button onClick={() => navigate(locationScope ? `/tools/identical?source=locations&location=${locationScope.id}` : "/tools/identical")}>
          Find identical files
        </Button>
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
              value={locationScope && !duplicates ? "current" : String(location?.id ?? "all")}
              disabled={!!locationScope && !duplicates}
              onChange={(event) => setLocation(options.find((item) => String(item.id) === event.target.value))}
            >
              {locationScope && !duplicates && <MenuItem value="current">{locationScope.name}</MenuItem>}
              <MenuItem value="all">All locations</MenuItem>
              {options.map((item) => (
                <MenuItem key={String(item.id)} value={String(item.id)}>
                  {item.name}
                </MenuItem>
              ))}
            </TextField>
            {showLocations && (more || loading) && !error && (
              <Button disabled={loading} onClick={loadMore}>
                {loading ? "Loading locations…" : "More locations"}
              </Button>
            )}
            <TextField select label="Archived content" value={status} onChange={(event) => setStatus(event.target.value)}>
              {archiveFilters.map((item) => (
                <MenuItem key={item.value} value={item.value}>
                  {item.label}
                </MenuItem>
              ))}
            </TextField>
            <FormControlLabel
              control={<Checkbox checked={duplicates} onChange={(_, checked) => setDuplicates(checked)} />}
              label="Duplicates in Locations"
              slotProps={{ typography: { sx: { fontSize: 12 } } }}
            />
            {showLocations && error && (
              <Feedback
                severity="warning"
                action={
                  <Button disabled={loading} onClick={retry}>
                    Retry
                  </Button>
                }
              >
                {error}
              </Feedback>
            )}
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button variant="contained" onClick={apply}>
            Apply filters
          </Button>
          <Button onClick={() => setOpen(false)}>Cancel</Button>
        </DialogActions>
      </Dialog>
    </>
  );
};

import { type ReactNode, useEffect, useRef, useState } from "react";
import { Alert, Autocomplete, Box, Button, CircularProgress, Paper, type PaperProps, Stack, TextField, Typography } from "@mui/material";
import { cli, locationCli } from "@/api";
import { Location, Media, MediaKind, OnlineBinding } from "@/entity";
import { errorMessage } from "@/tools";

const pageSize = 30;
const SearchPaper = ({ children, footer, ...props }: PaperProps & { footer?: ReactNode }) => (
  <Paper {...props}>
    {children}
    {footer}
  </Paper>
);

type SelectionProps<T> = { value: T | null; onChange: (item: T | null) => void; disabled?: boolean };
type SearchProps<T> = SelectionProps<T> & {
  label: string;
  placeholder: string;
  name: (item: T) => string;
  detail: (item: T) => string;
  unavailable?: (item: T) => boolean;
  search: (query: string, afterId: bigint, signal: AbortSignal) => Promise<{ items: T[]; hasMore: boolean }>;
};

const CatalogSearchSelect = <T extends { id: bigint }>({
  value,
  onChange,
  disabled = false,
  label,
  placeholder,
  name,
  detail,
  unavailable,
  search,
}: SearchProps<T>) => {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [options, setOptions] = useState<T[]>([]);
  const [cursors, setCursors] = useState([0n]);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  const root = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const paper = useRef<HTMLDivElement>(null);
  const request = useRef(0);
  const afterId = cursors.at(-1) ?? 0n;
  const optionLabel = (item: T) => `${name(item)} · ${detail(item)}`;

  useEffect(() => {
    const pending = request;
    const generation = ++pending.current;
    if (!open || disabled) return;
    const controller = new AbortController();
    setLoading(true);
    setError("");
    setOptions([]);
    setHasMore(false);
    const timer = window.setTimeout(async () => {
      try {
        const reply = await search(query.trim(), afterId, controller.signal);
        if (pending.current !== generation) return;
        setOptions(reply.items);
        setHasMore(reply.hasMore);
      } catch (error) {
        if (pending.current !== generation) return;
        setError(errorMessage(error, `Could not search ${label}`));
      } finally {
        if (pending.current === generation) setLoading(false);
      }
    }, 250);
    return () => {
      ++pending.current;
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [open, disabled, query, afterId, retry, search, label]);

  const footer = (
    <Stack spacing={1} sx={{ p: 1, borderTop: 1, borderColor: "divider" }}>
      {error && (
        <Alert
          severity="error"
          action={
            <Button
              onClick={() => {
                input.current?.focus();
                setRetry((current) => current + 1);
              }}
            >
              Retry
            </Button>
          }
        >
          {error}
        </Alert>
      )}
      {!error && (cursors.length > 1 || hasMore) && (
        <Stack direction="row" sx={{ alignItems: "center", justifyContent: "space-between" }}>
          <Button
            disabled={loading || cursors.length === 1}
            onClick={() => {
              input.current?.focus();
              setCursors((current) => current.slice(0, -1));
            }}
          >
            Previous
          </Button>
          <Typography variant="caption">Page {cursors.length}</Typography>
          <Button
            disabled={loading || !hasMore}
            onClick={() => {
              input.current?.focus();
              setCursors((current) => [...current, options.at(-1)!.id]);
            }}
          >
            Next
          </Button>
        </Stack>
      )}
    </Stack>
  );
  const paperProps: PaperProps & { footer?: ReactNode } = {
    ref: paper,
    footer: error || cursors.length > 1 || hasMore ? footer : undefined,
    onMouseDown: (event) => event.preventDefault(),
    onBlur: (event) => {
      if (paper.current?.contains(event.relatedTarget) || root.current?.contains(event.relatedTarget)) return;
      setOpen(false);
    },
    onKeyDown: (event) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      setOpen(false);
      input.current?.focus();
    },
  };
  // Keep popup controls in the field's tab order; fixed positioning avoids clipping by surrounding cards.
  return (
    <Autocomplete
      ref={root}
      fullWidth
      disablePortal
      disabled={disabled}
      value={value}
      inputValue={value ? optionLabel(value) : query}
      open={open && !disabled}
      options={options}
      loading={loading}
      loadingText="Searching…"
      noOptionsText={error ? "Search unavailable" : `No matching ${label}`}
      filterOptions={(values) => values}
      getOptionLabel={optionLabel}
      getOptionKey={(item) => String(item.id)}
      getOptionDisabled={unavailable}
      isOptionEqualToValue={(option, selected) => option.id === selected.id}
      onOpen={() => setOpen(true)}
      onClose={(event, reason) => {
        if (reason === "blur" && paper.current?.contains((event as React.FocusEvent).relatedTarget)) return;
        setOpen(false);
      }}
      onInputChange={(_, text, reason) => {
        if (reason !== "input" && reason !== "clear") return;
        if (text !== query || afterId !== 0n) {
          ++request.current;
          setOptions([]);
          setHasMore(false);
        }
        setCursors([0n]);
        setQuery(text);
        onChange(null);
      }}
      onChange={(_, selected) => {
        onChange(selected);
        setQuery("");
        setCursors([0n]);
      }}
      slots={{ paper: SearchPaper }}
      slotProps={{ paper: paperProps, popper: { popperOptions: { strategy: "fixed" } } }}
      renderOption={(props, item) => {
        const { key, ...optionProps } = props;
        return (
          <Box component="li" key={key} {...optionProps}>
            <Box sx={{ minWidth: 0 }}>
              <Typography sx={{ overflowWrap: "anywhere" }}>{name(item)}</Typography>
              <Typography variant="caption" color="text.secondary" sx={{ overflowWrap: "anywhere" }}>
                {detail(item)}
              </Typography>
            </Box>
          </Box>
        );
      }}
      renderInput={(params) => (
        <TextField
          {...params}
          inputRef={input}
          required
          label={label}
          placeholder={placeholder}
          slotProps={{
            ...params.slotProps,
            input: {
              ...params.slotProps.input,
              endAdornment: (
                <>
                  {loading && open && <CircularProgress color="inherit" size={18} aria-label={`Searching ${label}`} />}
                  {params.slotProps.input.endAdornment}
                </>
              ),
            },
          }}
        />
      )}
    />
  );
};

const searchMedia = async (query: string, afterId: bigint, signal: AbortSignal) => {
  const reply = await cli.mediaList(
    { param: { oneofKind: "list", list: { kinds: [MediaKind.VOLUME, MediaKind.TAPE], query, afterId, limit: BigInt(pageSize) } } },
    { abort: signal },
  ).response;
  return { items: reply.media, hasMore: reply.hasMore };
};
const searchLocations = async (query: string, afterId: bigint, signal: AbortSignal) => {
  const reply = await locationCli.list({ query, afterId, limit: pageSize }, { abort: signal }).response;
  return { items: reply.locations, hasMore: reply.hasMore };
};

export const MediaSearchSelect = (props: SelectionProps<Media>) => (
  <CatalogSearchSelect
    {...props}
    label="Media"
    placeholder="Search by name or identity"
    search={searchMedia}
    name={(item) => item.name || item.identity || `#${item.id}`}
    detail={(item) =>
      [item.kind === MediaKind.TAPE ? "Tape" : "Volume", item.name && item.name !== item.identity ? item.identity || `#${item.id}` : ""]
        .filter(Boolean)
        .join(" · ")
    }
  />
);
export const LocationSearchSelect = (props: SelectionProps<Location>) => (
  <CatalogSearchSelect
    {...props}
    label="Location"
    placeholder="Search by name or path"
    search={searchLocations}
    name={(item) => item.name}
    detail={(item) => `${item.rootPath}${item.binding === OnlineBinding.UNCONFIRMED ? " · Confirm imported path" : ""}`}
    unavailable={(item) => item.binding !== OnlineBinding.CONFIRMED}
  />
);

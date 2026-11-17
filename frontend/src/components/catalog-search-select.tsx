import { t } from "@lingui/core/macro";
import { Feedback } from "@/components/feedback";
import { useEffect, useRef, useState } from "react";
import { Autocomplete, Box, Button, CircularProgress, Stack, TextField, Typography } from "@mui/material";
import { mediaCli } from "@/api";
import { Location, Media, MediaKind } from "@/entity";
import { errorMessage } from "@/tools";
import { SearchPaper, type SearchPaperProps } from "./search-paper";
import { useLocationChoices } from "./use-location-choices";

const pageSize = 30;

type SelectionProps<T> = { value: T | null; onChange: (item: T | null) => void; disabled?: boolean };
type SearchProps<T> = SelectionProps<T> & {
  label: string;
  noMatchText: string;
  searchingLabel: string;
  placeholder: string;
  name: (item: T) => string;
  detail: (item: T) => string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  query: string;
  onQueryChange: (query: string) => void;
  options: T[];
  loading: boolean;
  error: string;
  onRetry: () => void;
  paging?: { page: number; hasMore: boolean; previous: () => void; next: () => void };
};

/** One popup presentation for the configured Location catalog and paged Media searches. */
const CatalogSearchSelect = <T extends { id: bigint }>({
  value,
  onChange,
  disabled = false,
  label,
  noMatchText,
  searchingLabel,
  placeholder,
  name,
  detail,
  open,
  onOpenChange,
  query,
  onQueryChange,
  options,
  loading,
  error,
  onRetry,
  paging,
}: SearchProps<T>) => {
  const root = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const paper = useRef<HTMLDivElement>(null);
  const page = paging ? String(paging.page) : "";
  const optionLabel = (item: T) => `${name(item)} · ${detail(item)}`;
  const footer = (
    <>
      {error && (
        <Feedback
          severity="error"
          action={
            <Button
              disabled={loading}
              onClick={() => {
                input.current?.focus();
                onRetry();
              }}
            >
              {t`Retry`}
            </Button>
          }
        >
          {error}
        </Feedback>
      )}
      {!error && paging && (
        <Stack direction="row" sx={{ alignItems: "center", justifyContent: "space-between" }}>
          <Button
            disabled={loading || paging.page === 1}
            onClick={() => {
              input.current?.focus();
              paging.previous();
            }}
          >
            {t`Previous`}
          </Button>
          <Typography variant="caption">{t`Page ${page}`}</Typography>
          <Button
            disabled={loading || !paging.hasMore}
            onClick={() => {
              input.current?.focus();
              paging.next();
            }}
          >
            {t`Next`}
          </Button>
        </Stack>
      )}
    </>
  );
  const paperProps: SearchPaperProps = {
    ref: paper,
    footer: error || paging ? footer : undefined,
    onMouseDown: (event) => event.preventDefault(),
    onBlur: (event) => {
      if (paper.current?.contains(event.relatedTarget) || root.current?.contains(event.relatedTarget)) return;
      onOpenChange(false);
    },
    onKeyDown: (event) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      onOpenChange(false);
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
      loadingText={t`Searching…`}
      noOptionsText={error ? t`Search unavailable` : noMatchText}
      filterOptions={(values) => values}
      getOptionLabel={optionLabel}
      getOptionKey={(item) => String(item.id)}
      isOptionEqualToValue={(option, selected) => option.id === selected.id}
      onOpen={() => onOpenChange(true)}
      onClose={(event, reason) => {
        if (reason === "blur" && paper.current?.contains((event as React.FocusEvent).relatedTarget)) return;
        onOpenChange(false);
      }}
      onInputChange={(_, text, reason) => {
        if (reason !== "input" && reason !== "clear") return;
        onQueryChange(text);
        onChange(null);
      }}
      onChange={(_, selected) => {
        onChange(selected);
        onQueryChange("");
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
                  {loading && open && <CircularProgress color="inherit" size={18} aria-label={searchingLabel} />}
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

const allMediaKinds = [MediaKind.VOLUME, MediaKind.TAPE];

/** Searches registered Media; `kinds` must be a stable array, restricting the search to those kinds. */
export const MediaSearchSelect = ({ kinds = allMediaKinds, ...props }: SelectionProps<Media> & { kinds?: MediaKind[] }) => {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [options, setOptions] = useState<Media[]>([]);
  const [cursors, setCursors] = useState([0n]);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  const request = useRef(0);
  const failed = useRef(false);
  const loaded = useRef<{ query: string; afterId: bigint } | undefined>(undefined);
  const afterId = cursors.at(-1) ?? 0n;
  const { disabled } = props;
  useEffect(() => {
    const pending = request;
    const generation = ++pending.current;
    if (!open || disabled || failed.current) return;
    if (loaded.current?.query === query && loaded.current.afterId === afterId) return;
    loaded.current = undefined;
    const controller = new AbortController();
    setLoading(true);
    setError("");
    setOptions([]);
    setHasMore(false);
    const timer = window.setTimeout(async () => {
      try {
        const reply = await mediaCli.list(
          { param: { oneofKind: "list", list: { kinds, query: query.trim(), afterId, limit: BigInt(pageSize) } } },
          { abort: controller.signal },
        ).response;
        if (pending.current !== generation) return;
        loaded.current = { query, afterId };
        setOptions(reply.media);
        setHasMore(reply.hasMore);
      } catch (error) {
        if (pending.current !== generation) return;
        failed.current = true;
        setError(errorMessage(error, t`Could not search Media`));
      } finally {
        if (pending.current === generation) setLoading(false);
      }
    }, 250);
    return () => {
      ++pending.current;
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [open, disabled, query, afterId, retry, kinds]);
  return (
    <CatalogSearchSelect
      {...props}
      label={t`Media`}
      noMatchText={t`No matching Media`}
      searchingLabel={t`Searching Media`}
      placeholder={t`Search by name or identity`}
      open={open}
      onOpenChange={setOpen}
      query={query}
      onQueryChange={(text) => {
        // The search effect replaces the options for the new query or first page.
        if (text !== query || afterId !== 0n) failed.current = false;
        setCursors([0n]);
        setQuery(text);
      }}
      options={options}
      loading={loading}
      error={error}
      onRetry={() => {
        failed.current = false;
        setRetry((current) => current + 1);
      }}
      paging={
        cursors.length > 1 || hasMore
          ? {
              page: cursors.length,
              hasMore,
              previous: () => setCursors((current) => current.slice(0, -1)),
              next: () => setCursors((current) => [...current, options.at(-1)!.id]),
            }
          : undefined
      }
      name={(item) => item.name || item.identity || `#${item.id}`}
      detail={(item) =>
        [item.kind === MediaKind.TAPE ? t`Tape` : t`Volume`, item.name && item.name !== item.identity ? item.identity || `#${item.id}` : ""]
          .filter(Boolean)
          .join(" · ")
      }
    />
  );
};

export const LocationSearchSelect = (props: SelectionProps<Location>) => {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const { locations, loading, error, retry } = useLocationChoices({ enabled: open && !props.disabled, query });
  return (
    <CatalogSearchSelect
      {...props}
      value={locations.find((location) => location.id === props.value?.id) ?? props.value}
      label={t`Location`}
      noMatchText={t`No matching Location`}
      searchingLabel={t`Searching Location`}
      placeholder={t`Search by name or path`}
      open={open}
      onOpenChange={setOpen}
      query={query}
      onQueryChange={setQuery}
      options={locations}
      loading={loading}
      error={error}
      onRetry={retry}
      name={(item) => item.name}
      detail={(item) => item.rootPath}
    />
  );
};

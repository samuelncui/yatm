import { useEffect, useMemo, useRef, useState } from "react";
import { Autocomplete, Box, Button, Chip, TextField } from "@mui/material";
import { locationCli } from "@/api";
import type { IdenticalRoot } from "@/entity";
import { Feedback } from "./feedback";
import { SearchPaper, type SearchPaperProps } from "./search-paper";
import { useLocationChoices } from "./use-location-choices";

type ScopeChoice = { key: string; label: string; locationId: bigint };

/** The Identical-files Location scope: search by name and see the selected Locations as chips. */
export const IdenticalScopePicker = ({
  value,
  onChange,
  disabled,
}: {
  value: IdenticalRoot[];
  onChange: (roots: IdenticalRoot[]) => void;
  disabled?: boolean;
}) => {
  const [known, setKnown] = useState<Map<string, string>>(() => new Map());
  const knownNames = useRef(known);
  const selectedIds = [...new Set(value.map((root) => String(root.locationId)))].join(",");
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const paper = useRef<HTMLDivElement>(null);
  const { locations: found, more, loading, error, loadMore, retry } = useLocationChoices({ enabled: open && !disabled, query });
  useEffect(() => {
    if (found.length) setKnown((current) => new Map([...current, ...found.map((location) => [String(location.id), location.name] as const)]));
  }, [found]);
  useEffect(() => {
    knownNames.current = known;
  }, [known]);
  // Deep links select IDs before the lazy chooser has supplied their names.
  useEffect(() => {
    const controller = new AbortController();
    const hydrate = async () => {
      for (const key of selectedIds ? selectedIds.split(",") : []) {
        if (controller.signal.aborted) return;
        if (knownNames.current.has(key)) continue;
        try {
          const { location } = await locationCli.get({ id: BigInt(key) }, { abort: controller.signal }).response;
          if (controller.signal.aborted) return;
          if (location) setKnown((current) => (current.has(key) ? current : new Map(current).set(key, location.name)));
        } catch {
          // An unavailable selected Location keeps its explicit ID label.
        }
      }
    };
    void hydrate();
    return () => controller.abort();
  }, [selectedIds]);
  const options = useMemo<ScopeChoice[]>(() => {
    const name = (locationId: bigint) => known.get(String(locationId)) ?? `Location ${locationId}`;
    const items = found.map((location) => ({ key: String(location.id), label: location.name, locationId: location.id }));
    const keys = new Set(items.map((item) => item.key));
    for (const root of value) {
      const key = String(root.locationId);
      if (keys.has(key)) continue;
      items.push({
        key,
        label: name(root.locationId),
        locationId: root.locationId,
      });
    }
    return items;
  }, [found, known, value]);
  // Every chosen Location stays an option, so a search never hides the scope being reported.
  const selected = value.flatMap((root) => options.filter((option) => option.key === String(root.locationId)));
  const footer = error ? (
    <Feedback
      severity="error"
      action={
        <Button
          disabled={loading}
          onClick={() => {
            input.current?.focus();
            retry();
          }}
        >
          Retry Locations
        </Button>
      }
    >
      {error}
    </Feedback>
  ) : more ? (
    <Button
      disabled={loading}
      onClick={() => {
        input.current?.focus();
        loadMore();
      }}
    >
      More Locations
    </Button>
  ) : undefined;
  const paperProps: SearchPaperProps = {
    ref: paper,
    footer,
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
  return (
    <Box ref={root} sx={{ display: "flex", alignItems: "center", gap: 1, flexGrow: 1, minWidth: 280 }}>
      <Autocomplete
        multiple
        disablePortal
        size="small"
        disabled={disabled}
        open={open && !disabled}
        onOpen={() => setOpen(true)}
        onClose={(event, reason) => {
          if (reason === "blur" && paper.current?.contains((event as React.FocusEvent).relatedTarget)) return;
          setOpen(false);
        }}
        options={options}
        value={selected}
        inputValue={query}
        loading={loading}
        loadingText="Loading Locations…"
        sx={{ flexGrow: 1, minWidth: 240 }}
        getOptionLabel={(option) => option.label}
        getOptionKey={(option) => option.key}
        isOptionEqualToValue={(option, candidate) => option.key === candidate.key}
        filterOptions={(items) => items}
        onInputChange={(_, input, reason) => {
          if (reason === "input" || reason === "clear") setQuery(input);
        }}
        onChange={(_, next) => {
          onChange(next.map((choice) => ({ locationId: choice.locationId })));
          setQuery("");
        }}
        slots={{ paper: SearchPaper }}
        slotProps={{ paper: paperProps, popper: { popperOptions: { strategy: "fixed" } } }}
        renderInput={(params) => <TextField {...params} inputRef={input} label="Locations" placeholder="Search by name" />}
        renderValue={(items, getItemProps) =>
          items.map((item, index) => <Chip {...getItemProps({ index })} key={item.key} label={item.label} title={item.label} />)
        }
        noOptionsText={error ? "Locations unavailable" : query ? "No matching Location" : "No Location registered"}
      />
    </Box>
  );
};

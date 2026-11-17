import { t } from "@lingui/core/macro";
import { useMemo, useRef, useState } from "react";
import { Autocomplete, Box, Button, Chip, TextField } from "@mui/material";
import { useAppSelector } from "@/state/react";
import { selectLocations } from "@/state/locations";
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
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const paper = useRef<HTMLDivElement>(null);
  const { locations: found, loading, error, retry } = useLocationChoices({ enabled: open && !disabled, query });
  // Selected Locations take their names from the shared catalog the application preloads, including deep links.
  const catalog = useAppSelector(selectLocations);
  const options = useMemo<ScopeChoice[]>(() => {
    const name = (locationId: bigint) => {
      const id = String(locationId);
      return catalog.find((location) => location.id === locationId)?.name ?? t`Location ${id}`;
    };
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
  }, [catalog, found, value]);
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
          {t`Retry Locations`}
        </Button>
      }
    >
      {error}
    </Feedback>
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
        loadingText={t`Loading Locations…`}
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
        renderInput={(params) => <TextField {...params} inputRef={input} label={t`Locations`} placeholder={t`Search by name`} />}
        renderValue={(items, getItemProps) =>
          items.map((item, index) => <Chip {...getItemProps({ index })} key={item.key} label={item.label} title={item.label} />)
        }
        noOptionsText={error ? t`Locations unavailable` : query ? t`No matching Location` : t`No Location registered`}
      />
    </Box>
  );
};

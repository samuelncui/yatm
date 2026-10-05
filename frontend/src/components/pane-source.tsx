import { useState } from "react";
import { Box, Button, IconButton, ListSubheader, Menu, MenuItem } from "@mui/material";
import ArrowDropDown from "@mui/icons-material/ArrowDropDown";
import { readStored, type StorageCodec } from "@/state/storage";
import { Feedback } from "./feedback";
import { useLocationChoices } from "./use-location-choices";

export type PaneSource = { kind: "library" } | { kind: "location"; id: string; name: string };
export const librarySource: PaneSource = { kind: "library" };
export const sourceKey = (source: PaneSource) => (source.kind === "library" ? "library" : `location:${source.id}`);

const paneSourceCodec: StorageCodec<PaneSource> = {
  encode: (value) => JSON.stringify(value),
  decode: (raw) => {
    const stored = JSON.parse(raw) as PaneSource | null;
    if (stored?.kind === "location" && typeof stored.id === "string" && /^[1-9]\d*$/.test(stored.id) && typeof stored.name === "string") return stored;
    return librarySource;
  },
};

export function storedPaneSource(key: string): PaneSource {
  return readStored("local", `${key}:source`, paneSourceCodec) ?? librarySource;
}

export const PaneSourceSelector = ({
  source,
  onChange,
  onNavigateRoot,
}: {
  source: PaneSource;
  onChange: (source: PaneSource) => void;
  onNavigateRoot: () => void;
}) => {
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const { locations, more, loading, error, loadMore, retry } = useLocationChoices({ enabled: !!anchor });
  const close = () => setAnchor(null);
  const select = (value: PaneSource) => {
    close();
    onChange(value);
  };
  return (
    <>
      <Box component="span" sx={{ display: "inline-flex", alignItems: "center", maxWidth: "100%", verticalAlign: "middle" }}>
        <Button
          size="small"
          color="inherit"
          onClick={onNavigateRoot}
          aria-label={`Go to ${source.kind === "library" ? "Library" : source.name} root`}
          sx={{ textTransform: "none", fontSize: 13, fontWeight: 650, minWidth: 0, maxWidth: 200, height: 28, px: 0.25 }}
        >
          <Box component="span" sx={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
            {source.kind === "library" ? "Library" : source.name}
          </Box>
        </Button>
        <IconButton
          size="small"
          color="inherit"
          sx={{ height: 28, width: 22, borderRadius: 1 }}
          aria-label="Choose file source"
          aria-haspopup="menu"
          aria-expanded={!!anchor}
          onClick={(event) => setAnchor(event.currentTarget)}
        >
          <ArrowDropDown fontSize="small" />
        </IconButton>
      </Box>
      <Menu anchorEl={anchor} open={!!anchor} onClose={close} slotProps={{ paper: { sx: { maxHeight: 420, minWidth: 240 } } }}>
        <MenuItem selected={source.kind === "library"} onClick={() => select(librarySource)}>
          Library
        </MenuItem>
        <ListSubheader>Locations</ListSubheader>
        {locations.map((location) => (
          <MenuItem
            key={String(location.id)}
            selected={source.kind === "location" && source.id === String(location.id)}
            onClick={() => select({ kind: "location", id: String(location.id), name: location.name })}
          >
            {location.name}
          </MenuItem>
        ))}
        {more && !error && (
          <MenuItem disabled={loading} onClick={loadMore}>
            Load more…
          </MenuItem>
        )}
        {error && (
          <Box component="li" role="presentation" sx={{ px: 1 }}>
            <Feedback severity="error">{error}</Feedback>
          </Box>
        )}
        {error && <MenuItem onClick={retry}>Retry locations</MenuItem>}
        {!locations.length && loading && <MenuItem disabled>Loading Locations…</MenuItem>}
        {!locations.length && !loading && !error && <MenuItem disabled>No locations</MenuItem>}
      </Menu>
    </>
  );
};

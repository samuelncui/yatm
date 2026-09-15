import { useCallback, useEffect, useState } from "react";
import { Box, Button, IconButton, ListSubheader, Menu, MenuItem } from "@mui/material";
import ArrowDropDown from "@mui/icons-material/ArrowDropDown";
import { locationCli } from "@/api";
import type { Location } from "@/entity";
import { runUIAction } from "@/tools";

export type PaneSource = { kind: "library" } | { kind: "location"; id: string; name: string };
export const librarySource: PaneSource = { kind: "library" };
export const sourceKey = (source: PaneSource) => (source.kind === "library" ? "library" : `location:${source.id}`);

export function storedPaneSource(key: string): PaneSource {
  try {
    const stored = JSON.parse(localStorage.getItem(`${key}:source`) ?? "null") as PaneSource | null;
    if (stored?.kind === "location" && /^[1-9]\d*$/.test(stored.id) && typeof stored.name === "string") return stored;
  } catch {
    /* Missing or obsolete browser preferences use the logical Library. */
  }
  return librarySource;
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
  const [locations, setLocations] = useState<Location[]>([]);
  const [more, setMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const load = useCallback(async (afterId = 0n) => {
    setLoading(true);
    try {
      const page = await locationCli.list({ afterId, limit: 50, query: "" }).response;
      setLocations((current) => (afterId ? [...current, ...page.locations] : page.locations));
      setMore(page.hasMore);
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => {
    if (anchor) runUIAction(() => load(), "Could not load locations");
  }, [anchor, load]);
  const select = (value: PaneSource) => {
    setAnchor(null);
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
      <Menu anchorEl={anchor} open={!!anchor} onClose={() => setAnchor(null)} slotProps={{ paper: { sx: { maxHeight: 420, minWidth: 240 } } }}>
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
        {more && (
          <MenuItem disabled={loading} onClick={() => runUIAction(() => load(locations.at(-1)?.id), "Could not load locations")}>
            Load more…
          </MenuItem>
        )}
        {!locations.length && !loading && <MenuItem disabled>No locations</MenuItem>}
      </Menu>
    </>
  );
};

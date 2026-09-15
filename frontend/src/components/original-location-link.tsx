import { Box, Chip, Link } from "@mui/material";
import { Link as RouterLink } from "react-router";
import type { Location } from "@/entity";

export type LocationNavigation = { id: string; path: string; reveal: string };

export function locationBrowserURL(id: bigint, path = "", reveal = "") {
  const query = new URLSearchParams({ location: String(id) });
  if (path) query.set("path", path);
  if (reveal) query.set("reveal", reveal);
  return `/file?${query}`;
}

export const OriginalLocationLink = ({ location, path }: { location: Pick<Location, "id" | "name" | "rootPath">; path: string }) => {
  const parent = path.slice(0, Math.max(0, path.lastIndexOf("/")));
  return (
    <Box sx={{ display: "flex", alignItems: "center", flexWrap: "wrap", gap: 1, minWidth: 0 }}>
      <Chip
        component={RouterLink}
        to={locationBrowserURL(location.id)}
        label={location.name}
        title={location.rootPath}
        clickable
        size="small"
        variant="outlined"
      />
      <Link component={RouterLink} to={locationBrowserURL(location.id, parent, path)} underline="hover" sx={{ overflowWrap: "anywhere", minWidth: 0 }}>
        {path || "/"}
      </Link>
    </Box>
  );
};

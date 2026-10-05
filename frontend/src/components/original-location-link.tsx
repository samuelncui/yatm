import { Box, Link } from "@mui/material";
import { Link as RouterLink } from "react-router";
import type { Location } from "@/entity";

export type LocationNavigation = { id: string; path: string; reveal: string };

export function locationBrowserURL(id: bigint, path = "", reveal = "") {
  const query = new URLSearchParams({ location: String(id) });
  if (path) query.set("path", path);
  if (reveal) query.set("reveal", reveal);
  return `/file?${query}`;
}

export function revealLocationFileURL(id: bigint, path: string) {
  return locationBrowserURL(id, path.slice(0, Math.max(0, path.lastIndexOf("/"))), path);
}

export const OriginalLocationLink = ({ location, path }: { location: Pick<Location, "id" | "name" | "rootPath">; path: string }) => {
  return (
    <Box component="span" sx={{ overflowWrap: "anywhere" }}>
      <Link component={RouterLink} to={locationBrowserURL(location.id)} title={location.rootPath} underline="hover">
        {location.name}
      </Link>
      {" / "}
      <Link component={RouterLink} to={revealLocationFileURL(location.id, path)} underline="hover">
        {path || "/"}
      </Link>
    </Box>
  );
};

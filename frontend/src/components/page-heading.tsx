import { ReactNode } from "react";
import Stack from "@mui/material/Stack";
import Typography from "@mui/material/Typography";

/**
 * The heading of a page: its name, what it applies to, and the actions that belong to the whole
 * page. Every page uses it, so a title cannot end up in a different size, place or weight on one
 * page than on another.
 */
export const PageHeading = ({ title, description, actions }: { title: string; description?: ReactNode; actions?: ReactNode }) => (
  <Stack spacing={1}>
    <Stack sx={{ minWidth: 0 }}>
      <Typography component="h1" variant="h5" sx={{ fontWeight: 650 }}>
        {title}
      </Typography>
      {description && (
        <Typography variant="body2" color="text.secondary">
          {description}
        </Typography>
      )}
    </Stack>
    {actions && (
      <Stack direction="row" spacing={1} sx={{ alignItems: "center", flexWrap: "wrap" }}>
        {actions}
      </Stack>
    )}
  </Stack>
);

import type { ReactNode } from "react";
import { Paper, Stack, type PaperProps } from "@mui/material";

export type SearchPaperProps = PaperProps & { footer?: ReactNode };

/** Search controls share presentation; each selector owns its query and keyboard behavior. */
export const SearchPaper = ({ children, footer, ...props }: SearchPaperProps) => (
  <Paper {...props}>
    {children}
    {footer && (
      <Stack spacing={1} sx={{ p: 1, borderTop: 1, borderColor: "divider" }}>
        {footer}
      </Stack>
    )}
  </Paper>
);

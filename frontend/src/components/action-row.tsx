import type { ReactNode } from "react";
import Stack from "@mui/material/Stack";

/** Inline actions stay together and scroll instead of shrinking on narrow screens. */
export const ActionRow = ({ children, className, "aria-label": ariaLabel }: { children: ReactNode; className?: string; "aria-label"?: string }) => (
  <Stack
    direction="row"
    className={className}
    aria-label={ariaLabel}
    sx={{ minWidth: 0, maxWidth: "100%", justifyContent: "flex-start", alignItems: "center", gap: "8px", overflowX: "auto", "& > *": { flexShrink: 0 } }}
  >
    {children}
  </Stack>
);

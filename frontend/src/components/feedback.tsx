import type { ReactNode } from "react";
import { ThemeProvider } from "@mui/material/styles";
import { theme } from "@/theme";
import { Alert, alertClasses, buttonClasses, type AlertColor } from "@mui/material";

/** Inline feedback owns message wrapping, action alignment and surrounding space. */
export const Feedback = ({ children, action, severity = "error" }: { children: ReactNode; action?: ReactNode; severity?: AlertColor }) => {
  const controls = typeof action === "boolean" ? undefined : action;
  return (
    <ThemeProvider theme={theme}>
      <Alert
        severity={severity}
        action={controls}
        sx={{
          display: "grid",
          gridTemplateColumns: { xs: "auto minmax(0, 1fr)", sm: controls != null ? "auto minmax(0, 1fr) auto" : "auto minmax(0, 1fr)" },
          alignItems: "start",
          columnGap: 1.5,
          rowGap: 1,
          minWidth: 0,
          flexShrink: 0,
          textAlign: "left",
          my: 1.5,
          px: 2,
          py: 1,
          [`& .${alertClasses.icon}`]: { m: 0, mt: "3px", p: 0 },
          [`& .${alertClasses.message}`]: { minWidth: 0, px: 0, py: 0.5, lineHeight: "20px", overflowWrap: "anywhere" },
          [`& .${alertClasses.action}`]: { gridColumn: { xs: 2, sm: 3 }, m: 0, p: 0, alignItems: "center" },
          [`& .${alertClasses.action} .${buttonClasses.root}`]: { py: 0.5, lineHeight: "20px", whiteSpace: "nowrap" },
        }}
      >
        {children}
      </Alert>
    </ThemeProvider>
  );
};

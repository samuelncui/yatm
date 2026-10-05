import { createTheme } from "@mui/material/styles";

export const theme = createTheme({
  palette: {
    primary: { main: "#2563eb" },
    secondary: { main: "#14b8a6" },
    background: { default: "#f3f6fa", paper: "#ffffff" },
    text: { primary: "#172033", secondary: "#667085" },
    divider: "#e1e7ef",
  },
  shape: { borderRadius: 10 },
  typography: {
    fontFamily: 'Inter, ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
    button: { fontWeight: 650, textTransform: "none" },
  },
  components: {
    MuiButton: {
      defaultProps: { disableElevation: true },
      styleOverrides: { root: { borderRadius: 8 } },
    },
    MuiCard: {
      defaultProps: { variant: "outlined" },
    },
    MuiDialog: {
      styleOverrides: {
        paper: { borderRadius: 14, backgroundImage: "none" },
      },
    },
    MuiDialogActions: {
      styleOverrides: { root: { justifyContent: "flex-start" } },
    },
    MuiLinearProgress: {
      styleOverrides: {
        root: { height: 6, borderRadius: 999, backgroundColor: "#e8eef7" },
        bar: { borderRadius: 999 },
      },
    },
  },
});

import { Box, Tabs, tabClasses, tabsClasses, ToggleButtonGroup, toggleButtonClasses, styled, type SxProps, type TabsProps, type Theme } from "@mui/material";

export const SidebarTabs = styled(Tabs)({
  [`& .${tabsClasses.indicator}`]: { display: "none" },
  [`& .${tabsClasses.list}`]: { gap: 6 },
  [`& .${tabClasses.root}`]: {
    width: "100%",
    minWidth: 0,
    minHeight: 44,
    padding: "10px 12px",
    justifyContent: "flex-start",
    borderRadius: 9,
    color: "#aebdd0",
    fontSize: 14,
    fontWeight: 600,
    textTransform: "none",
    transition: "color 140ms ease, background-color 140ms ease",
    "& svg": { marginRight: 10, fontSize: 19 },
    "&:hover": { color: "#fff", background: "rgba(255, 255, 255, 0.07)" },
    [`&.${tabClasses.selected}`]: { color: "#fff", background: "rgba(45, 212, 191, 0.16)", boxShadow: "inset 0 0 0 1px rgba(94, 234, 212, 0.16)" },
    "@media (max-width: 900px)": { width: 48, minWidth: 48, padding: 10, justifyContent: "center", fontSize: 0, "& svg": { margin: 0 } },
  },
});

export const LibraryLayoutToggle = styled(ToggleButtonGroup)(({ theme }) => ({
  marginLeft: "auto",
  padding: 2,
  border: `1px solid ${theme.palette.divider}`,
  borderRadius: 8,
  background: "#f8fafc",
  [`& .${toggleButtonClasses.root}`]: {
    width: 28,
    height: 28,
    minHeight: 0,
    padding: 0,
    border: 0,
    borderRadius: 6,
    color: theme.palette.text.secondary,
    background: "transparent",
    "& svg": { fontSize: 16 },
    "&:hover": { color: theme.palette.text.primary, background: "#eef2f6" },
    [`&.${toggleButtonClasses.selected}`]: {
      color: theme.palette.primary.main,
      background: theme.palette.background.paper,
      boxShadow: "0 1px 3px rgba(15, 23, 42, 0.12)",
      "&:hover": { background: theme.palette.background.paper },
    },
  },
}));

/**
 * Where a tab row sits. Module is the navigation strip above a page, compact is the secondary
 * row beside it, section is a row inside a page, and detail is the compact Inspector row.
 * One component owns all four placements so
 * spacing, sizing and states cannot drift from page to page again.
 */
export type PageTabsPlacement = "module" | "compact" | "section" | "detail";

const placementStyles = {
  module: {
    minHeight: 56,
    [`& .${tabsClasses.list}`]: { gap: 0.5 },
    [`& .${tabClasses.root}`]: {
      minWidth: 72,
      minHeight: 56,
      px: 1.75,
      color: "text.secondary",
      fontSize: 14,
      fontWeight: 650,
      textTransform: "none",
      [`&.${tabClasses.selected}`]: { color: "primary.main" },
    },
    [`& .${tabsClasses.indicator}`]: { height: 3, borderTopLeftRadius: 3, borderTopRightRadius: 3 },
  },
  compact: {
    minHeight: 38,
    [`& .${tabClasses.root}`]: {
      minWidth: 68,
      minHeight: 38,
      px: 1.5,
      borderRadius: "8px",
      color: "text.secondary",
      fontSize: 14,
      fontWeight: 650,
      textTransform: "none",
      [`&.${tabClasses.selected}`]: { color: "primary.main", backgroundColor: "#eaf1ff" },
    },
    [`& .${tabsClasses.indicator}`]: { display: "none" },
  },
  section: {
    mt: 1.75,
    borderBottom: 1,
    borderColor: "divider",
    [`& .${tabClasses.root}`]: {
      fontWeight: 650,
      textTransform: "none",
      color: "text.secondary",
      [`&.${tabClasses.selected}`]: { color: "primary.main" },
    },
  },
  detail: {
    flex: "0 0 auto",
    minHeight: 42,
    mt: 0,
    padding: "0 18px",
    borderBottom: "1px solid #e1e7ef",
    bgcolor: "#fff",
    [`& .${tabClasses.root}`]: {
      minWidth: 0,
      minHeight: 42,
      padding: 0,
      fontSize: 12,
      fontWeight: 650,
      textTransform: "none",
      color: "text.secondary",
      [`&.${tabClasses.selected}`]: { color: "primary.main" },
    },
  },
} satisfies Record<PageTabsPlacement, SxProps<Theme>>;

export const PageTabs = ({ placement = "section", sx, ...props }: TabsProps & { placement?: PageTabsPlacement }) => (
  <Tabs {...props} sx={[placementStyles[placement], ...(Array.isArray(sx) ? sx : [sx])]} />
);

/** The strip above a page's content: one row that holds the page's tab row and its own controls. */
export const PageNavigation = ({ label, children }: { label: string; children: React.ReactNode }) => (
  <Box
    component="nav"
    aria-label={label}
    sx={{
      minHeight: 56,
      px: 3,
      display: "flex",
      flex: "0 0 auto",
      alignItems: "center",
      gap: 2,
      overflowX: "auto",
      borderBottom: 1,
      borderColor: "divider",
      bgcolor: "rgba(255, 255, 255, 0.94)",
    }}
  >
    {children}
  </Box>
);

/** Separates two tab rows that share one navigation strip. */
export const PageNavigationDivider = () => <Box sx={{ width: "1px", height: 24, flex: "0 0 auto", bgcolor: "divider" }} />;

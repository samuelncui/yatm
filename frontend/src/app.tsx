import { lazy, Suspense, SyntheticEvent, useCallback, useEffect, useState } from "react";
import { JobListStateProvider } from "@/components/job-list-state";
import { Routes, Route, useNavigate, Navigate, useLocation, Link } from "react-router";

import Inventory2RoundedIcon from "@mui/icons-material/Inventory2Rounded";
import PreviewRoundedIcon from "@mui/icons-material/PreviewRounded";
import SettingsRoundedIcon from "@mui/icons-material/SettingsRounded";
import ViewColumnRoundedIcon from "@mui/icons-material/ViewColumnRounded";
import WorkHistoryRoundedIcon from "@mui/icons-material/WorkHistoryRounded";
import CssBaseline from "@mui/material/CssBaseline";
import LinearProgress from "@mui/material/LinearProgress";
import Tab from "@mui/material/Tab";
import Tabs from "@mui/material/Tabs";
import ToggleButton from "@mui/material/ToggleButton";
import ToggleButtonGroup from "@mui/material/ToggleButtonGroup";
import { createTheme, styled, ThemeProvider } from "@mui/material/styles";
import { ToastContainer, toast } from "react-toastify";
import "react-toastify/dist/ReactToastify.css";

import {
  BackupType,
  FileBrowserType,
  JobsType,
  jobListPath,
  type LibraryLayout,
  libraryLayouts,
  MediaType,
  LocationsType,
  RestoreType,
  ScanType,
  SettingsType,
} from "@/pages/routes";
import logoURL from "../favicon.svg";

import "./app.less";
import "./product.less";

const FileBrowser = lazy(async () => ({ default: (await import("@/pages/file")).FileBrowser }));
const BackupBrowser = lazy(async () => ({ default: (await import("@/pages/backup")).BackupBrowser }));
const RestoreBrowser = lazy(async () => ({ default: (await import("@/pages/restore")).RestoreBrowser }));
const ScanBrowser = lazy(async () => ({ default: (await import("@/pages/scan")).ScanBrowser }));
const MediaBrowser = lazy(async () => ({ default: (await import("@/pages/media")).MediaBrowser }));
const LocationsBrowser = lazy(async () => ({ default: (await import("@/pages/online")).LocationsBrowser }));
const JobsBrowser = lazy(async () => ({ default: (await import("@/pages/jobs")).JobsBrowser }));
const SettingsBrowser = lazy(async () => ({ default: (await import("@/pages/settings")).SettingsBrowser }));

const theme = createTheme({
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
    MuiLinearProgress: {
      styleOverrides: {
        root: { height: 6, borderRadius: 999, backgroundColor: "#e8eef7" },
        bar: { borderRadius: 999 },
      },
    },
  },
});

const NewJobType = "new-job";
const libraryLayoutStorageKey = "library:file-layout";

const navigation = [
  { label: "Library", value: FileBrowserType, icon: <Inventory2RoundedIcon /> },
  { label: "Jobs", value: JobsType, icon: <WorkHistoryRoundedIcon /> },
  { label: "Settings", value: SettingsType, icon: <SettingsRoundedIcon /> },
];

const moduleByPage: Record<string, string> = {
  [FileBrowserType]: FileBrowserType,
  [MediaType]: FileBrowserType,
  [JobsType]: JobsType,
  [BackupType]: JobsType,
  [RestoreType]: JobsType,
  [ScanType]: JobsType,
  [SettingsType]: SettingsType,
};

const version = (import.meta.env.VITE_YATM_VERSION || "development").replace(/^v/, "");

const ErrorMessage = styled("p")({
  margin: 0,
  textAlign: "left",
});

const ModuleNavigation = ({
  page,
  libraryLayout,
  onLibraryLayoutChange,
}: {
  page: string;
  libraryLayout: LibraryLayout;
  onLibraryLayoutChange: (layout: LibraryLayout) => void;
}) => {
  const navigate = useNavigate();
  const location = useLocation();
  const openPage = (_: SyntheticEvent, value: string) => navigate("/" + value);

  if (page === SettingsType) {
    return (
      <nav className="app-module-navigation" aria-label="Settings views">
        <Tabs className="app-module-tabs" value={location.pathname === "/settings/library" ? "settings/library" : LocationsType} onChange={openPage}>
          <Tab label="Locations" value={LocationsType} />
          <Tab label="Library" value="settings/library" />
        </Tabs>
      </nav>
    );
  }

  if (page === FileBrowserType || page === MediaType) {
    const changeLayout = (_: SyntheticEvent, value: LibraryLayout | null) => {
      if (!value) return;
      onLibraryLayoutChange(value);
    };

    return (
      <nav className="app-module-navigation" aria-label="Library views">
        <Tabs className="app-module-tabs" value={page} onChange={openPage}>
          <Tab label="Files" value={FileBrowserType} />
          <Tab label="Media" value={MediaType} />
        </Tabs>
        {page === FileBrowserType && (
          <ToggleButtonGroup className="library-layout-toggle" value={libraryLayout} exclusive onChange={changeLayout} aria-label="File browser layout">
            <ToggleButton value={libraryLayouts.dual} aria-label="Dual pane" title="Dual pane">
              <ViewColumnRoundedIcon />
            </ToggleButton>
            <ToggleButton value={libraryLayouts.inspector} aria-label="Inspector" title="Inspector">
              <PreviewRoundedIcon />
            </ToggleButton>
          </ToggleButtonGroup>
        )}
      </nav>
    );
  }

  const creatingJob = page === BackupType || page === RestoreType || page === ScanType;
  if (page !== JobsType && !creatingJob) return null;

  return (
    <nav className="app-module-navigation" aria-label="Job views">
      <Tabs className="app-module-tabs" value={creatingJob ? NewJobType : JobsType}>
        <Tab component={Link} to={jobListPath(location.state?.returnTo)} label="All" value={JobsType} />
        <Tab component={Link} to={"/" + BackupType} label="New" value={NewJobType} />
      </Tabs>
      {creatingJob && (
        <>
          <span className="app-module-navigation-divider" />
          <Tabs className="app-module-tabs app-module-tabs-secondary" value={page} onChange={openPage}>
            <Tab label="Backup" value={BackupType} />
            <Tab label="Restore" value={RestoreType} />
            <Tab label="Scan" value={ScanType} />
          </Tabs>
        </>
      )}
    </nav>
  );
};

const App = () => {
  const location = useLocation();
  const [libraryLayout, setLibraryLayout] = useState<LibraryLayout>(() =>
    localStorage.getItem(libraryLayoutStorageKey) === libraryLayouts.dual ? libraryLayouts.dual : libraryLayouts.inspector,
  );
  const currentPage = location.pathname.split("/")[1] || FileBrowserType;
  const currentModule = moduleByPage[currentPage] ?? FileBrowserType;
  const changeLibraryLayout = useCallback((layout: LibraryLayout) => {
    setLibraryLayout(layout);
    localStorage.setItem(libraryLayoutStorageKey, layout);
  }, []);

  useEffect(() => {
    const origin = window.onunhandledrejection;
    window.onunhandledrejection = (error) => {
      if (error.reason.name !== "RpcError") {
        return;
      }

      console.log("rpc request have error:", error);
      toast.error(
        <div>
          <ErrorMessage>
            <b>RPC Request Error</b>
          </ErrorMessage>
          <ErrorMessage>
            <b>Method: </b>
            {error.reason.methodName}
          </ErrorMessage>
          <ErrorMessage>
            <b>Message: </b>
            {error.reason.message}
          </ErrorMessage>
        </div>,
      );
    };
    return () => {
      window.onunhandledrejection = origin;
    };
  }, []);

  return (
    <ThemeProvider theme={theme}>
      <CssBaseline />
      <div id="app">
        <aside className="app-sidebar">
          <div className="app-brand">
            <span className="app-brand-mark">
              <img src={logoURL} alt="" />
            </span>
            <span className="app-brand-copy">
              <strong>YATM</strong>
              <small>Media archive</small>
            </span>
          </div>
          <Tabs className="tabs" value={currentModule} orientation="vertical" aria-label="YATM modules">
            {navigation.map((item) => (
              <Tab
                key={item.value}
                component={Link}
                to={item.value === JobsType ? jobListPath(location.state?.returnTo) : "/" + item.value}
                icon={item.icon}
                iconPosition="start"
                label={item.label}
                value={item.value}
              />
            ))}
          </Tabs>
          <span className="app-sidebar-caption">YATM {version}</span>
        </aside>
        <main className="app-content">
          <ModuleNavigation page={currentPage} libraryLayout={libraryLayout} onLibraryLayoutChange={changeLibraryLayout} />
          <div className="app-page">
            <JobListStateProvider>
              <Suspense fallback={<LinearProgress className="app-page-loading" />}>
                <Routes>
                  <Route path="/*">
                    <Route path={FileBrowserType} element={<FileBrowser layout={libraryLayout} />} />
                    <Route path={BackupType} element={<BackupBrowser />} />
                    <Route path={RestoreType} element={<RestoreBrowser />} />
                    <Route path={ScanType} element={<ScanBrowser />} />
                    <Route path={MediaType} element={<MediaBrowser />} />
                    <Route path={LocationsType + "/*"} element={<LocationsBrowser />} />
                    <Route path={JobsType} element={<JobsBrowser />} />
                    <Route path={JobsType + "/:id"} element={<JobsBrowser />} />
                    <Route path={SettingsType} element={<Navigate to={"/" + LocationsType} replace />} />
                    <Route path="settings/library" element={<SettingsBrowser />} />
                    <Route path="*" element={<Navigate to={"/" + FileBrowserType} replace />} />
                  </Route>
                </Routes>
              </Suspense>
            </JobListStateProvider>
          </div>
        </main>
        <ToastContainer autoClose={10000} />
      </div>
    </ThemeProvider>
  );
};

export default App;

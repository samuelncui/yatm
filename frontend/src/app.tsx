import { readStored, writeStored, stringCodec } from "@/state/storage";
import { lazy, Suspense, SyntheticEvent, useCallback, useEffect, useState } from "react";
import { LibraryLayoutToggle, PageNavigation, PageNavigationDivider, PageTabs, SidebarTabs } from "@/components/page-navigation";
import { Routes, Route, useNavigate, Navigate, useLocation, Link } from "react-router";

import Inventory2RoundedIcon from "@mui/icons-material/Inventory2Rounded";
import PreviewRoundedIcon from "@mui/icons-material/PreviewRounded";
import SettingsRoundedIcon from "@mui/icons-material/SettingsRounded";
import ViewColumnRoundedIcon from "@mui/icons-material/ViewColumnRounded";
import WorkHistoryRoundedIcon from "@mui/icons-material/WorkHistoryRounded";
import CssBaseline from "@mui/material/CssBaseline";
import LinearProgress from "@mui/material/LinearProgress";
import Tab from "@mui/material/Tab";
import ToggleButton from "@mui/material/ToggleButton";
import { styled, ThemeProvider } from "@mui/material/styles";
import { theme } from "@/theme";
import { ToastContainer, toast } from "react-toastify";
import "react-toastify/dist/ReactToastify.css";

import {
  ArchiveType,
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

const IdenticalFiles = lazy(async () => ({ default: (await import("@/pages/identical")).IdenticalFiles }));
const FileBrowser = lazy(async () => ({ default: (await import("@/pages/file")).FileBrowser }));
const ArchiveBrowser = lazy(async () => ({ default: (await import("@/pages/archive")).ArchiveBrowser }));
const RestoreBrowser = lazy(async () => ({ default: (await import("@/pages/restore")).RestoreBrowser }));
const ScanBrowser = lazy(async () => ({ default: (await import("@/pages/scan")).ScanBrowser }));
const MediaBrowser = lazy(async () => ({ default: (await import("@/pages/media")).MediaBrowser }));
const LocationsBrowser = lazy(async () => ({ default: (await import("@/pages/locations")).LocationsBrowser }));
const JobsBrowser = lazy(async () => ({ default: (await import("@/pages/jobs")).JobsBrowser }));
const SettingsBrowser = lazy(async () => ({ default: (await import("@/pages/settings")).SettingsBrowser }));
const PreviewSettingsBrowser = lazy(async () => ({ default: (await import("@/pages/settings-preview")).PreviewSettingsBrowser }));
const JobSettingsBrowser = lazy(async () => ({ default: (await import("@/pages/settings-jobs")).JobSettingsBrowser }));

const NewJobType = "new-job";
const libraryLayoutStorageKey = "library:file-layout";

const navigation = [
  { label: "Library", value: FileBrowserType, icon: <Inventory2RoundedIcon /> },
  { label: "Tools", value: "tools/identical", icon: <ViewColumnRoundedIcon /> },
  { label: "Jobs", value: JobsType, icon: <WorkHistoryRoundedIcon /> },
  { label: "Settings", value: SettingsType, icon: <SettingsRoundedIcon /> },
];

const moduleByPage: Record<string, string> = {
  tools: "tools/identical",
  [FileBrowserType]: FileBrowserType,
  [MediaType]: FileBrowserType,
  [JobsType]: JobsType,
  [ArchiveType]: JobsType,
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

  if (page === "tools")
    return (
      <PageNavigation label="Tools">
        <PageTabs placement="module" value="identical">
          <Tab value="identical" label="Identical files" />
        </PageTabs>
      </PageNavigation>
    );

  if (page === SettingsType) {
    return (
      <PageNavigation label="Settings views">
        <PageTabs
          placement="module"
          value={["/settings/library", "/settings/preview", "/settings/jobs"].includes(location.pathname) ? location.pathname.slice(1) : LocationsType}
          onChange={openPage}
        >
          <Tab label="Locations" value={LocationsType} />
          <Tab label="Library" value="settings/library" />
          <Tab label="Preview" value="settings/preview" />
          <Tab label="Jobs" value="settings/jobs" />
        </PageTabs>
      </PageNavigation>
    );
  }

  if (page === FileBrowserType || page === MediaType) {
    const changeLayout = (_: SyntheticEvent, value: LibraryLayout | null) => {
      if (!value) return;
      onLibraryLayoutChange(value);
    };

    return (
      <PageNavigation label="Library views">
        <PageTabs placement="module" value={page} onChange={openPage}>
          <Tab label="Files" value={FileBrowserType} />
          <Tab label="Media" value={MediaType} />
        </PageTabs>
        {page === FileBrowserType && (
          <LibraryLayoutToggle className="library-layout-toggle" value={libraryLayout} exclusive onChange={changeLayout} aria-label="File browser layout">
            <ToggleButton value={libraryLayouts.dual} aria-label="Dual pane" title="Dual pane">
              <ViewColumnRoundedIcon />
            </ToggleButton>
            <ToggleButton value={libraryLayouts.inspector} aria-label="Inspector" title="Inspector">
              <PreviewRoundedIcon />
            </ToggleButton>
          </LibraryLayoutToggle>
        )}
      </PageNavigation>
    );
  }

  const creatingJob = page === ArchiveType || page === RestoreType || page === ScanType;
  if (page !== JobsType && !creatingJob) return null;

  return (
    <PageNavigation label="Job views">
      <PageTabs placement="module" value={creatingJob ? NewJobType : JobsType}>
        <Tab component={Link} to={jobListPath(location.state?.returnTo)} label="All" value={JobsType} />
        <Tab component={Link} to={"/" + ArchiveType} label="New" value={NewJobType} />
      </PageTabs>
      {creatingJob && (
        <>
          <PageNavigationDivider />
          <PageTabs placement="compact" value={page} onChange={openPage}>
            <Tab label="Archive" value={ArchiveType} />
            <Tab label="Restore" value={RestoreType} />
            <Tab label="Scan" value={ScanType} />
          </PageTabs>
        </>
      )}
    </PageNavigation>
  );
};

const App = () => {
  const location = useLocation();
  const [libraryLayout, setLibraryLayout] = useState<LibraryLayout>(() =>
    readStored("local", libraryLayoutStorageKey, stringCodec) === libraryLayouts.dual ? libraryLayouts.dual : libraryLayouts.inspector,
  );
  const currentPage = location.pathname.split("/")[1] || FileBrowserType;
  const currentModule = moduleByPage[currentPage] ?? FileBrowserType;
  const changeLibraryLayout = useCallback((layout: LibraryLayout) => {
    setLibraryLayout(layout);
    writeStored("local", libraryLayoutStorageKey, layout, stringCodec);
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
          <SidebarTabs className="tabs" value={currentModule} orientation="vertical" aria-label="YATM modules">
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
          </SidebarTabs>
          <span className="app-sidebar-caption">YATM {version}</span>
        </aside>
        <main className="app-content">
          <ModuleNavigation page={currentPage} libraryLayout={libraryLayout} onLibraryLayoutChange={changeLibraryLayout} />
          <div className="app-page">
            <Suspense fallback={<LinearProgress className="app-page-loading" />}>
              <Routes>
                <Route path="/*">
                  <Route path="tools/identical" element={<IdenticalFiles />} />
                  <Route path={FileBrowserType} element={<FileBrowser layout={libraryLayout} />} />
                  <Route path={ArchiveType} element={<ArchiveBrowser />} />
                  <Route path={RestoreType} element={<RestoreBrowser />} />
                  <Route path={ScanType} element={<ScanBrowser />} />
                  <Route path={MediaType} element={<MediaBrowser />} />
                  <Route path={LocationsType + "/*"} element={<LocationsBrowser />} />
                  <Route path={JobsType} element={<JobsBrowser />} />
                  <Route path={JobsType + "/:id"} element={<JobsBrowser />} />
                  <Route path={SettingsType} element={<Navigate to={"/" + LocationsType} replace />} />
                  <Route path="settings/library" element={<SettingsBrowser />} />
                  <Route path="settings/preview" element={<PreviewSettingsBrowser />} />
                  <Route path="settings/jobs" element={<JobSettingsBrowser />} />
                  <Route path="*" element={<Navigate to={"/" + FileBrowserType} replace />} />
                </Route>
              </Routes>
            </Suspense>
          </div>
        </main>
        <ToastContainer autoClose={10000} />
      </div>
    </ThemeProvider>
  );
};

export default App;

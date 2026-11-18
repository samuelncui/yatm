import { i18n } from "@lingui/core";
import { msg, t } from "@lingui/core/macro";
import { readStored, writeStored, stringCodec } from "@/state/storage";
import { lazy, Suspense, SyntheticEvent, useCallback, useEffect, useMemo, useState } from "react";
import { LibraryLayoutToggle, PageNavigation, PageNavigationDivider, PageTabs, SidebarTabs } from "@/components/page-navigation";
import { SettingsEditorPage } from "@/components/settings-page";
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
import { createAppTheme } from "@/theme";
import { useLocale } from "@/i18n";
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
import { errorMessage } from "@/tools";

const IdenticalFiles = lazy(async () => ({ default: (await import("@/pages/identical")).IdenticalFiles }));
const FileBrowser = lazy(async () => ({ default: (await import("@/pages/file")).FileBrowser }));
const ArchiveBrowser = lazy(async () => ({ default: (await import("@/pages/archive")).ArchiveBrowser }));
const RestoreBrowser = lazy(async () => ({ default: (await import("@/pages/restore")).RestoreBrowser }));
const ScanBrowser = lazy(async () => ({ default: (await import("@/pages/scan")).ScanBrowser }));
const MediaBrowser = lazy(async () => ({ default: (await import("@/pages/media")).MediaBrowser }));
const LocationsBrowser = lazy(async () => ({ default: (await import("@/pages/locations")).LocationsBrowser }));
const JobsBrowser = lazy(async () => ({ default: (await import("@/pages/jobs")).JobsBrowser }));
const GeneralSettingsBrowser = lazy(async () => ({ default: (await import("@/pages/settings-general")).GeneralSettingsBrowser }));
const SettingsBrowser = lazy(async () => ({ default: (await import("@/pages/settings")).SettingsBrowser }));
const PreviewSettingsBrowser = lazy(async () => ({ default: (await import("@/pages/settings-preview")).PreviewSettingsBrowser }));
const JobSettingsBrowser = lazy(async () => ({ default: (await import("@/pages/settings-jobs")).JobSettingsBrowser }));

const DeviceSettingsBrowser = lazy(async () => ({ default: (await import("@/pages/settings-devices")).DeviceSettingsBrowser }));

const NewJobType = "new-job";
const libraryLayoutStorageKey = "library:file-layout";

const navigation = [
  { label: msg`Library`, value: FileBrowserType, icon: <Inventory2RoundedIcon /> },
  { label: msg`Tools`, value: "tools/identical", icon: <ViewColumnRoundedIcon /> },
  { label: msg`Jobs`, value: JobsType, icon: <WorkHistoryRoundedIcon /> },
  { label: msg`Settings`, value: SettingsType, icon: <SettingsRoundedIcon /> },
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
      <PageNavigation label={t`Tools`}>
        <PageTabs placement="module" value="identical">
          <Tab value="identical" label={t`Identical files`} />
        </PageTabs>
      </PageNavigation>
    );

  if (page === SettingsType) {
    const path = location.pathname.replace(/\/+$/, "");
    return (
      <PageNavigation label={t`Settings views`}>
        <PageTabs
          placement="module"
          value={
            ["/settings/general", "/settings/library", "/settings/preview", "/settings/jobs", "/settings/devices"].includes(path)
              ? path.slice(1)
              : LocationsType
          }
        >
          <Tab component={Link} to="/settings/general" label={t`General`} value="settings/general" />
          <Tab component={Link} to={"/" + LocationsType} label={t`Locations`} value={LocationsType} />
          <Tab component={Link} to="/settings/library" label={t`Library`} value="settings/library" />
          <Tab component={Link} to="/settings/preview" label={t`Preview`} value="settings/preview" />
          <Tab component={Link} to="/settings/jobs" label={t`Jobs`} value="settings/jobs" />
          <Tab component={Link} to="/settings/devices" label={t`Devices`} value="settings/devices" />
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
      <PageNavigation label={t`Library views`}>
        <PageTabs placement="module" value={page} onChange={openPage}>
          <Tab label={t`Files`} value={FileBrowserType} />
          <Tab label={t`Media`} value={MediaType} />
        </PageTabs>
        {page === FileBrowserType && (
          <LibraryLayoutToggle className="library-layout-toggle" value={libraryLayout} exclusive onChange={changeLayout} aria-label={t`File browser layout`}>
            <ToggleButton value={libraryLayouts.dual} aria-label={t`Dual pane`} title={t`Dual pane`}>
              <ViewColumnRoundedIcon />
            </ToggleButton>
            <ToggleButton value={libraryLayouts.inspector} aria-label={t`Inspector`} title={t`Inspector`}>
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
    <PageNavigation label={t`Job views`}>
      <PageTabs placement="module" value={creatingJob ? NewJobType : JobsType}>
        <Tab component={Link} to={jobListPath(location.state?.returnTo)} label={t`All`} value={JobsType} />
        <Tab component={Link} to={"/" + ArchiveType} label={t`New`} value={NewJobType} />
      </PageTabs>
      {creatingJob && (
        <>
          <PageNavigationDivider />
          <PageTabs placement="compact" value={page} onChange={openPage}>
            <Tab label={t`Archive`} value={ArchiveType} />
            <Tab label={t`Restore`} value={RestoreType} />
            <Tab label={t`Scan`} value={ScanType} />
          </PageTabs>
        </>
      )}
    </PageNavigation>
  );
};

const App = () => {
  const location = useLocation();
  const { locale } = useLocale();
  const theme = useMemo(() => createAppTheme(locale), [locale]);
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
            <b>{t`RPC Request Error`}</b>
          </ErrorMessage>
          <ErrorMessage>
            <b>{t`Method: `}</b>
            {error.reason.methodName}
          </ErrorMessage>
          <ErrorMessage>
            <b>{t`Message: `}</b>
            {errorMessage(error.reason, t`Request failed`)}
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
              <small>{t`Media archive`}</small>
            </span>
          </div>
          <SidebarTabs className="tabs" value={currentModule} orientation="vertical" aria-label={t`YATM modules`}>
            {navigation.map((item) => (
              <Tab
                key={item.value}
                component={Link}
                to={item.value === JobsType ? jobListPath(location.state?.returnTo) : "/" + item.value}
                icon={item.icon}
                iconPosition="start"
                label={i18n._(item.label)}
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
                  <Route path={SettingsType} element={<Navigate to="/settings/general" replace />} />
                  <Route
                    path="settings/general"
                    element={
                      <SettingsEditorPage key="general" title={t`General`} description={t`Interface language and which confirmations appear before an action.`}>
                        <GeneralSettingsBrowser />
                      </SettingsEditorPage>
                    }
                  />
                  <Route
                    path="settings/library"
                    element={
                      <SettingsEditorPage key="library" title={t`Library`} description={t`What Library shows and where its data can go.`}>
                        <SettingsBrowser />
                      </SettingsEditorPage>
                    }
                  />
                  <Route
                    path="settings/preview"
                    element={
                      <SettingsEditorPage
                        key="preview"
                        title={t`Preview`}
                        description={t`Runtime controls apply to the next file; routes and output choices apply to new Jobs.`}
                      >
                        <PreviewSettingsBrowser />
                      </SettingsEditorPage>
                    }
                  />
                  <Route
                    path="settings/jobs"
                    element={
                      <SettingsEditorPage
                        key="jobs"
                        title={t`Jobs`}
                        description={t`Pipeline limits for Archive, Restore and Scan Jobs, captured when the next attempt starts. Running attempts keep their current limits.`}
                      >
                        <JobSettingsBrowser />
                      </SettingsEditorPage>
                    }
                  />
                  <Route
                    path="settings/devices"
                    element={
                      <SettingsEditorPage
                        key="devices"
                        title={t`Devices`}
                        description={t`Defaults apply to later Tape FORMAT choices. Saving Device settings does not operate the device.`}
                      >
                        <DeviceSettingsBrowser />
                      </SettingsEditorPage>
                    }
                  />
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

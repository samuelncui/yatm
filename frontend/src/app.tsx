import { useCallback, ChangeEvent } from "react";
import { Routes, Route, useNavigate, Navigate, useLocation } from "react-router-dom";

import Tabs from "@mui/material/Tabs";
import Tab from "@mui/material/Tab";
import { createTheme, styled, ThemeProvider } from "@mui/material/styles";
import { ToastContainer, toast } from "react-toastify";
import "react-toastify/dist/ReactToastify.css";

import { FileBrowser, FileBrowserType } from "./pages/file";
import { BackupBrowser, BackupType } from "./pages/backup";
import { RestoreBrowser, RestoreType } from "./pages/restore";
import { TapesBrowser, TapesType } from "./pages/tapes";
import { JobsBrowser, JobsType } from "./pages/jobs";

import "./app.less";
import { sleep } from "./tools";
import { useEffect } from "react";
import { useState } from "react";

// import reactLogo from './assets/react.svg'
// <img src={reactLogo} className="logo react" alt="React logo" />

const theme = createTheme({});

const Delay = ({ inner }: { inner: JSX.Element }) => {
  const [ok, setOK] = useState(false);
  useEffect(() => {
    setOK(false);
    (async () => {
      await sleep(0);
      setOK(true);
    })();
    return () => {
      setOK(false);
    };
  }, [inner]);

  return ok ? inner : null;
};

const ErrorMessage = styled("p")({
  margin: 0,
  textAlign: "left",
});

const App = () => {
  const location = useLocation();
  const navigate = useNavigate();
  const handleTabChange = useCallback(
    (_: ChangeEvent<{}>, newValue: string) => {
      navigate("/" + newValue);
    },
    [navigate],
  );

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
    <div id="app">
      <ThemeProvider theme={theme}>
        <Tabs
          className="tabs"
          value={location.pathname.slice(1)}
          onChange={handleTabChange}
          indicatorColor="secondary"
          variant="scrollable"
          scrollButtons="auto"
          allowScrollButtonsMobile
        >
          <Tab label="File" value={FileBrowserType} />
          <Tab label="Backup" value={BackupType} />
          <Tab label="Restore" value={RestoreType} />
          <Tab label="Tapes" value={TapesType} />
          <Tab label="Jobs" value={JobsType} />
        </Tabs>
        <Routes>
          <Route path="/*">
            <Route path={FileBrowserType} element={<Delay inner={<FileBrowser />} />} />
            <Route path={BackupType} element={<Delay inner={<BackupBrowser />} />} />
            <Route path={RestoreType} element={<Delay inner={<RestoreBrowser />} />} />
            <Route path={TapesType} element={<Delay inner={<TapesBrowser />} />} />
            <Route path={JobsType} element={<Delay inner={<JobsBrowser />} />} />
            <Route path="*" element={<Navigate to={"/" + FileBrowserType} replace />} />
          </Route>
        </Routes>
      </ThemeProvider>
      <ToastContainer autoClose={10000} />
    </div>
  );
};

export default App;

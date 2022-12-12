import { useEffect } from "react";
import { useState, useCallback } from "react";
import { ChangeEvent } from "react";

import Tabs from "@mui/material/Tabs";
import Tab from "@mui/material/Tab";
import { createTheme, ThemeProvider, styled } from "@mui/material/styles";

import { FileBrowser, FileBrowserType } from "./file";
import { BackupBrowser, BackupType } from "./backup";
import { JobsBrowser, JobsType } from "./jobs";

import "./app.less";
import { sleep } from "./api";
import { Nullable } from "tsdef";

// import reactLogo from './assets/react.svg'
// <img src={reactLogo} className="logo react" alt="React logo" />

const theme = createTheme({});

const typeToElement = (type: string) => {
  switch (type) {
    case FileBrowserType:
      return <FileBrowser />;
    case BackupType:
      return <BackupBrowser />;
    case JobsType:
      return <JobsBrowser />;
    default:
      return null;
  }
};

const App = () => {
  const [tabValue, setTabValue] = useState(FileBrowserType);
  const [inner, setInner] = useState<Nullable<JSX.Element>>(null);

  const setType = useCallback(
    (newValue: string) => {
      (async () => {
        setTabValue(newValue);
        setInner(null);
        await sleep(0);
        setInner(typeToElement(newValue));
      })();
    },
    [setTabValue, setInner]
  );

  const handleTabChange = useCallback(
    (_: ChangeEvent<{}>, newValue: string) => {
      setType(newValue);
    },
    [setTabValue]
  );

  useEffect(() => {
    setType(FileBrowserType);
  }, []);

  return (
    <div id="app">
      <ThemeProvider theme={theme}>
        <Tabs className="tabs" value={tabValue} onChange={handleTabChange} indicatorColor="secondary">
          <Tab label="File" value={FileBrowserType} />
          <Tab label="Source" value={BackupType} />
          <Tab label="Jobs" value={JobsType} />
        </Tabs>
      </ThemeProvider>
      {inner}
    </div>
  );
};

export default App;

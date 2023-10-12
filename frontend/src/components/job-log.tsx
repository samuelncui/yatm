import { Fragment, useState, useRef, useEffect, useCallback } from "react";

import Button from "@mui/material/Button";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogTitle from "@mui/material/DialogTitle";

import { cli } from "../api";
import { sleep } from "../tools";

export const ViewLogDialog = ({ jobID }: { jobID: bigint }) => {
  const [open, setOpen] = useState(false);
  const handleClickOpen = () => {
    setOpen(true);
  };
  const handleClose = () => {
    setOpen(false);
  };

  return (
    <Fragment>
      <Button size="small" onClick={handleClickOpen}>
        View Log
      </Button>
      {open && (
        <Dialog open={true} onClose={handleClose} maxWidth={"lg"} fullWidth scroll="paper" sx={{ height: "100%" }} className="view-log-dialog">
          <DialogTitle>View Log</DialogTitle>
          <DialogContent dividers>
            <LogConsole jobId={jobID} />
          </DialogContent>
          <DialogActions>
            <Button onClick={handleClose}>Close</Button>
          </DialogActions>
        </Dialog>
      )}
    </Fragment>
  );
};

const LogConsole = ({ jobId }: { jobId: bigint }) => {
  const [log, setLog] = useState<string>("");
  const [offset, setOffset] = useState(0n);
  const bottom = useRef(null);

  const refresh = useCallback(async () => {
    const reply = await cli.jobGetLog({ jobId, offset: offset }).response;
    setLog(log + new TextDecoder().decode(reply.logs));
    setOffset(reply.offset);
  }, [log, setLog, offset, setOffset, bottom]);
  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;

  useEffect(() => {
    var timer: NodeJS.Timeout;
    (async () => {
      await refreshRef.current();
      if (bottom.current) {
        const bottomElem = bottom.current as HTMLElement;
        await sleep(10);
        bottomElem.scrollIntoView(true);
        await sleep(10);
        bottomElem.parentElement?.scrollBy(0, 100);
      }

      timer = setInterval(() => refreshRef.current(), 2000);
    })();

    return () => {
      if (!timer) {
        return;
      }

      clearInterval(timer);
    };
  }, [refresh]);

  return (
    <Fragment>
      <pre>{log || "loading..."}</pre>
      <div ref={bottom} />
    </Fragment>
  );
};

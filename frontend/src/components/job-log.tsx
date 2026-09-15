import { Fragment, useState, useRef, useEffect, useCallback, useEffectEvent } from "react";

import Button from "@mui/material/Button";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogTitle from "@mui/material/DialogTitle";

import { jobCli } from "@/api";
import { sleep } from "@/tools";

const maxVisibleLogLength = 4 * 1024 * 1024;

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
        <Dialog open onClose={handleClose} maxWidth="lg" fullWidth scroll="paper" className="job-view-dialog">
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
    const reply = await jobCli.getLog({ id: jobId, offset }).response;
    if (reply.logs && reply.logs.length > 0) {
      setLog((log + new TextDecoder().decode(reply.logs)).slice(-maxVisibleLogLength));
    }
    setOffset(reply.offset);
  }, [jobId, log, offset]);
  const refreshEvent = useEffectEvent(refresh);

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const poll = async () => {
      await refreshEvent();
      if (cancelled) return;
      timer = setTimeout(() => void poll(), 2000);
    };

    (async () => {
      await refreshEvent();
      if (cancelled) return;
      if (bottom.current) {
        const bottomElem = bottom.current as HTMLElement;
        await sleep(10);
        if (cancelled) return;
        bottomElem.scrollIntoView(true);
        await sleep(10);
        if (cancelled) return;
        bottomElem.parentElement?.scrollBy(0, 100);
      }

      timer = setTimeout(() => void poll(), 2000);
    })();

    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
  }, [jobId]);

  return (
    <Fragment>
      <pre>{log || "loading..."}</pre>
      <div ref={bottom} />
    </Fragment>
  );
};

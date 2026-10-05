import { useCallback, useEffect, useRef, useState } from "react";
import {
  Button,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  List,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  Stack,
  Typography,
} from "@mui/material";
import FolderOutlined from "@mui/icons-material/FolderOutlined";
import { locationCli } from "@/api";
import type { BrowsePathsResponse } from "@/entity";
import { Feedback } from "@/components/feedback";
import { errorMessage } from "@/tools";

export const LocationPathPicker = ({ initialPath, onChoose, onClose }: { initialPath: string; onChoose: (path: string) => void; onClose: () => void }) => {
  const [paths, setPaths] = useState(initialPath ? [initialPath] : []);
  const path = paths.at(-1) ?? "";
  const [page, setPage] = useState<BrowsePathsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<{ message: string; path: string; cursor: string } | null>(null);
  const request = useRef<AbortController | null>(null);
  const load = useCallback(
    async (cursor = "", requestPath = path) => {
      if (request.current) return;
      const controller = new AbortController();
      request.current = controller;
      setLoading(true);
      setError(null);
      try {
        const reply = await locationCli.browsePaths({ path: requestPath, cursor, limit: 100 }, { abort: controller.signal }).response;
        if (controller.signal.aborted) return;
        setPage((current) => (cursor && current ? { ...reply, directories: [...current.directories, ...reply.directories] } : reply));
      } catch (failure) {
        if (!controller.signal.aborted) setError({ message: errorMessage(failure, "Could not browse directory"), path: requestPath, cursor });
      } finally {
        if (!controller.signal.aborted) setLoading(false);
        if (request.current === controller) request.current = null;
      }
    },
    [path],
  );
  useEffect(() => {
    const pending = request;
    void load();
    return () => {
      pending.current?.abort();
      pending.current = null;
    };
  }, [load]);
  const navigate = (next: string[]) => {
    if ((next.at(-1) ?? "") === path) return;
    request.current?.abort();
    request.current = null;
    setPage(null);
    setError(null);
    setLoading(true);
    setPaths(next);
  };
  const close = () => {
    request.current?.abort();
    onClose();
  };
  return (
    <Dialog open onClose={close} maxWidth="sm" fullWidth scroll="paper">
      <DialogTitle>Choose directory</DialogTitle>
      <DialogContent dividers>
        <Stack direction="row" sx={{ alignItems: "center", gap: 1, flexWrap: "wrap" }}>
          <Button disabled={!path} onClick={() => navigate(paths.slice(0, -1))}>
            Back
          </Button>
          <Button disabled={!path} onClick={() => navigate([])}>
            Allowed directories
          </Button>
        </Stack>
        {path && <Typography sx={{ overflowWrap: "anywhere", my: 1 }}>{page?.path ?? path}</Typography>}
        {loading && <CircularProgress size={20} aria-label="Loading directories" />}
        {error && <Feedback action={<Button onClick={() => void load(error.cursor, error.path)}>Retry</Button>}>{error.message}</Feedback>}
        <List aria-label="Directories" disablePadding>
          {page?.directories.map((directory) => (
            <ListItemButton key={directory.path} onClick={() => navigate([...paths, directory.path])}>
              <ListItemIcon>
                <FolderOutlined />
              </ListItemIcon>
              <ListItemText primary={directory.name} slotProps={{ primary: { sx: { overflowWrap: "anywhere" } } }} />
            </ListItemButton>
          ))}
        </List>
        {!loading && !error && page?.directories.length === 0 && <Typography>No directories.</Typography>}
        {page?.nextCursor && (
          <Button disabled={loading || !!error} onClick={() => void load(page.nextCursor, page.path)}>
            Load more directories
          </Button>
        )}
      </DialogContent>
      <DialogActions>
        <Button variant="contained" disabled={loading || !!error || !page?.path} onClick={() => onChoose(page!.path)}>
          Choose directory
        </Button>
        <Button onClick={close}>Cancel</Button>
      </DialogActions>
    </Dialog>
  );
};

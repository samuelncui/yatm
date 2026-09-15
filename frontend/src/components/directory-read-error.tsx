import { Alert, AlertTitle, Box, Button } from "@mui/material";
import { runUIAction } from "@/tools";

export const DirectoryReadError = ({ error, onRetry }: { error: string; onRetry: () => Promise<void> }) => (
  <Alert severity="error" action={<Button onClick={() => runUIAction(onRetry, "Could not read this directory")}>Retry</Button>}>
    <AlertTitle>Could not read this directory</AlertTitle>
    <details>
      <summary>Details</summary>
      <Box component="pre" sx={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere", margin: "8px 0 0", font: "inherit" }}>
        {error}
      </Box>
    </details>
  </Alert>
);

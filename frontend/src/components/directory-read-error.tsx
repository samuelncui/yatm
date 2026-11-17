import { t } from "@lingui/core/macro";
import { Feedback } from "@/components/feedback";
import { AlertTitle, Box, Button } from "@mui/material";
import { runUIAction } from "@/tools";

export const DirectoryReadError = ({ error, onRetry }: { error: string; onRetry: () => Promise<void> }) => (
  <Feedback severity="error" action={<Button onClick={() => runUIAction(onRetry, t`Could not read this directory`)}>{t`Retry`}</Button>}>
    <AlertTitle>{t`Could not read this directory`}</AlertTitle>
    <details>
      <summary>{t`Details`}</summary>
      <Box component="pre" sx={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere", margin: "8px 0 0", font: "inherit" }}>
        {error}
      </Box>
    </details>
  </Feedback>
);

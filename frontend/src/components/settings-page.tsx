import { t } from "@lingui/core/macro";
import { Feedback } from "@/components/feedback";
import { type ReactNode, Suspense, useCallback, useId, useRef, useState } from "react";
import Card from "@mui/material/Card";
import CardContent from "@mui/material/CardContent";
import CardHeader from "@mui/material/CardHeader";
import Button from "@mui/material/Button";
import ButtonBase from "@mui/material/ButtonBase";
import Alert from "@mui/material/Alert";
import LinearProgress from "@mui/material/LinearProgress";
import Stack from "@mui/material/Stack";
import ExpandMoreRoundedIcon from "@mui/icons-material/ExpandMoreRounded";
import { ActionRow } from "@/components/action-row";
import { PageHeading } from "@/components/page-heading";
import { ActionDialog } from "@/components/action-dialog";
import { useBeforeUnload, useBlocker } from "react-router";

/**
 * A whole Settings surface: its heading and the settings it holds across the available width.
 * Surfaces compose sections; none of them brings its own frame.
 */
export const SettingsPage = ({ title, description, children }: { title: string; description?: ReactNode; children: ReactNode }) => (
  <Stack useFlexGap spacing={2} sx={{ width: "100%", height: "100%", minHeight: 0, p: 3, overflowY: "auto", textAlign: "left" }}>
    <PageHeading title={title} description={description} />
    {children}
  </Stack>
);

/**
 * One group of settings: its heading and controls.
 * The framework's card owns the frame, so every group looks like every other group.
 */
export const SettingsSection = ({
  title,
  description,
  children,
  collapsible = false,
  hasError = false,
}: {
  title?: string;
  description?: ReactNode;
  children?: ReactNode;
  collapsible?: boolean;
  hasError?: boolean;
}) => {
  const [expanded, setExpanded] = useState(false);
  const contentID = useId();
  const visible = !collapsible || expanded || hasError;
  return (
    <Card sx={{ overflow: "hidden" }}>
      {title && (
        <CardHeader
          component={collapsible ? ButtonBase : "div"}
          title={title}
          subheader={description}
          onClick={collapsible ? () => setExpanded((current) => !current) : undefined}
          aria-expanded={collapsible ? visible : undefined}
          aria-controls={collapsible ? contentID : undefined}
          action={collapsible && <ExpandMoreRoundedIcon sx={{ transform: visible ? "rotate(180deg)" : "none" }} />}
          slotProps={{
            title: { variant: "subtitle1", fontWeight: 650 },
            subheader: { variant: "body2" },
            action: { sx: { alignSelf: "center", m: 0 } },
          }}
          sx={{ width: "100%", textAlign: "left", px: 2.5, py: 1.5, borderBottom: visible ? 1 : 0, borderColor: "divider", bgcolor: "grey.50" }}
        />
      )}
      {children && (
        <CardContent id={contentID} hidden={!visible} sx={{ px: 2.5, py: 2, "&:last-child": { pb: 2 } }}>
          <Stack useFlexGap spacing={2}>
            {children}
          </Stack>
        </CardContent>
      )}
    </Card>
  );
};

/** The page-level commit controls shared by editable Settings pages. */
export const SettingsActions = ({
  dirty,
  loading,
  saving,
  onCancel,
  children,
}: {
  dirty: boolean;
  loading: boolean;
  saving: boolean;
  onCancel: () => void;
  children?: ReactNode;
}) => (
  <ActionRow>
    <Button variant="contained" type="submit" disabled={!dirty || saving || loading}>
      {saving ? t`Saving…` : t`Save`}
    </Button>
    <Button disabled={!dirty || saving || loading} onClick={onCancel}>
      {t`Cancel`}
    </Button>
    {children}
  </ActionRow>
);

/** One loading indicator for Settings editors and the configured Location list. */
export const SettingsLoading = ({ loading }: { loading: boolean }) => (loading ? <LinearProgress aria-label={t`Loading settings`} /> : null);

/** Keep the page frame in place while its editor code and then its values load. */
export const SettingsEditorPage = ({ title, description, children }: { title: string; description: string; children: ReactNode }) => (
  <SettingsPage title={title} description={description}>
    <Suspense fallback={<SettingsLoading loading />}>{children}</Suspense>
  </SettingsPage>
);

/** Shared feedback for loading and saving one Settings group. */
export const SettingsEditorFeedback = ({
  loading,
  saving,
  error,
  invalid = "",
  saved,
  dirty,
  hasDraft,
  onReload,
}: {
  loading: boolean;
  saving: boolean;
  error: string;
  invalid?: string;
  saved: boolean;
  dirty: boolean;
  hasDraft: boolean;
  onReload: () => void;
}) => (
  <>
    <UnsavedChangesPrompt when={() => dirty}>{t`Your settings have not been saved.`}</UnsavedChangesPrompt>
    <SettingsLoading loading={loading} />
    {error && (
      <Feedback
        severity="error"
        action={
          <Button color="inherit" disabled={loading || saving} onClick={onReload}>
            {hasDraft ? t`Reload saved settings` : t`Retry`}
          </Button>
        }
      >
        {error}
      </Feedback>
    )}
    {invalid && <Feedback severity="error">{invalid}</Feedback>}
    {saved && !dirty && <Alert severity="success">{t`Settings saved.`}</Alert>}
  </>
);

/** Confirms leaving a page with unsaved edits; `when` is read at the moment of navigation or unload. */
export const UnsavedChangesPrompt = ({ when, children }: { when: () => boolean; children: ReactNode }) => {
  const blocker = useBlocker(when);
  const confirmed = useRef(false);
  useBeforeUnload(
    useCallback(
      (event: BeforeUnloadEvent) => {
        if (!when()) return;
        event.preventDefault();
        event.returnValue = "";
      },
      [when],
    ),
  );
  if (blocker.state !== "blocked") return null;
  return (
    <ActionDialog
      title={t`Discard unsaved changes?`}
      confirmLabel={t`Discard`}
      cancelLabel={t`Keep editing`}
      danger
      onConfirm={() => {
        confirmed.current = true;
        blocker.proceed?.();
      }}
      onClose={() => {
        if (!confirmed.current) blocker.reset?.();
        confirmed.current = false;
      }}
    >
      {children}
    </ActionDialog>
  );
};

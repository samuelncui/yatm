import { useId, type ReactNode } from "react";
import { Box, CircularProgress, Dialog, dialogClasses, IconButton, styled, Tab } from "@mui/material";
import CloseRoundedIcon from "@mui/icons-material/CloseRounded";
import { PageTabs } from "./page-navigation";
import "./detail-surface.less";

export const DetailDialog = styled(Dialog)({
  [`& .${dialogClasses.paper}`]: { height: "min(740px, calc(100vh - 64px))", overflow: "hidden", display: "flex", flexDirection: "column" },
  [`& .${dialogClasses.paperFullScreen}`]: {
    height: "100%",
    maxHeight: "none",
    borderRadius: 0,
    "& .detail-surface": { border: 0, borderRadius: 0 },
  },
});

type DetailTab = { value: string; label: string };
type Props = {
  title: string;
  icon?: ReactNode;
  status?: { label: string; color: string };
  busy?: boolean;
  tabs?: DetailTab[];
  selectedTab?: string;
  onTabChange?: (value: string) => void;
  preview?: ReactNode;
  notice?: ReactNode;
  children?: ReactNode;
  onClose?: () => void;
  idPrefix?: string;
  titleId?: string;
};

// One stable header, navigation row, and scroll boundary for Inspector and Dialog details.
// Callers supply only entity-specific facts and operations.
export const DetailSurface = ({ title, titleId, icon, status, busy, tabs, selectedTab, onTabChange, preview, notice, children, onClose, idPrefix }: Props) => {
  const generatedID = useId();
  const prefix = idPrefix || generatedID;
  return (
    <section className="detail-surface" aria-busy={busy}>
      <header className="detail-surface-header">
        {icon && (
          <span className="detail-surface-icon" aria-hidden="true">
            {icon}
          </span>
        )}
        <div className="detail-surface-heading">
          <h2 id={titleId} title={title}>
            {title}
          </h2>
          {status && (
            <span className="detail-surface-status" title={status.label}>
              <Box component="i" sx={{ bgcolor: status.color }} />
              {status.label}
            </span>
          )}
        </div>
        {busy && <CircularProgress size={16} aria-label="Refreshing details" />}
        {onClose && (
          <IconButton size="small" aria-label="Close details" onClick={onClose}>
            <CloseRoundedIcon fontSize="small" />
          </IconButton>
        )}
      </header>
      {tabs && selectedTab && (
        <PageTabs
          placement="detail"
          className="detail-surface-tabs"
          value={selectedTab}
          onChange={(_, value: string) => onTabChange?.(value)}
          variant="fullWidth"
          aria-label={`${title} details`}
        >
          {tabs.map(({ value, label }) => (
            <Tab key={value} value={value} label={label} id={`${prefix}-tab-${value}`} aria-controls={`${prefix}-panel-${value}`} />
          ))}
        </PageTabs>
      )}
      <div className="detail-surface-scroll">
        {notice}
        {preview && (
          <div className="detail-surface-preview" aria-label="Preview">
            {preview}
          </div>
        )}
        {children}
      </div>
    </section>
  );
};

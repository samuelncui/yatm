import Button, { buttonClasses, type ButtonProps } from "@mui/material/Button";
import { styled } from "@mui/material/styles";

const ChoiceButton = styled(Button)(({ theme }) => ({
  display: "flex",
  alignItems: "center",
  justifyContent: "flex-start",
  gap: "8px",
  width: "100%",
  minWidth: 0,
  padding: "7px 9px",
  borderColor: "#e0e6ee",
  borderRadius: "8px",
  backgroundColor: theme.palette.background.paper,
  color: "#344054",
  font: "inherit",
  letterSpacing: "normal",
  textTransform: "none",
  textAlign: "left",
  "& > strong": { minWidth: 0, flex: "1 1 auto", fontSize: "12px", lineHeight: 1.4, fontWeight: 600 },
  "& small": { display: "block", color: theme.palette.text.secondary, fontSize: "12px", fontWeight: 400 },
  "& > small": { flex: "0 0 auto", fontSize: "11px", lineHeight: 1.4 },
  "&:hover": { borderColor: "#e0e6ee", backgroundColor: "#f6f8fc" },
  '&[aria-pressed="true"]': {
    borderColor: "#5586e8",
    backgroundColor: "#f1f6ff",
    boxShadow: "inset 3px 0 #3973dd",
    "&:hover": { borderColor: "#5586e8", backgroundColor: "#f1f6ff" },
  },
  [`&:focus-visible, &.${buttonClasses.focusVisible}`]: { outline: `2px solid ${theme.palette.primary.main}`, outlineOffset: "2px" },
  [`&.${buttonClasses.disabled}`]: {
    borderColor: theme.palette.divider,
    backgroundColor: theme.palette.action.disabledBackground,
    color: theme.palette.action.disabled,
    boxShadow: "none",
    "& small": { color: "inherit" },
  },
}));

/** Saved-version choices share presentation while callers own their content and selection. */
export const SavedVersionChoice = ({ selected, ...props }: Pick<ButtonProps, "children" | "disabled" | "onClick" | "aria-label"> & { selected: boolean }) => (
  <ChoiceButton {...props} type="button" variant="outlined" aria-pressed={selected} />
);

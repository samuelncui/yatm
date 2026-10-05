import { Feedback } from "@/components/feedback";
import { type ReactNode, useEffect, useRef, useState } from "react";
import Autocomplete from "@mui/material/Autocomplete";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import TextField from "@mui/material/TextField";

import { cli } from "@/api";
import { errorMessage } from "@/tools";
import { normalizeTags } from "./file-metadata";
import { SearchPaper, type SearchPaperProps } from "./search-paper";

type Props = {
  value: string[];
  onChange: (tags: string[]) => void;
  disabled?: boolean;
  error?: boolean;
  helperText?: ReactNode;
};

type Results = {
  query: string;
  names: string[];
  nextCursor: string;
  loading: boolean;
  error: string;
};

export const TagSelect = ({ value, onChange, disabled = false, error = false, helperText }: Props) => {
  const [open, setOpen] = useState(false);
  const [input, setInput] = useState("");
  const [results, setResults] = useState<Results>();
  const [retry, setRetry] = useState(0);
  const root = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const request = useRef(0);
  const query = input.trim().toLowerCase();
  const current = results?.query === query ? results : undefined;
  const names = current?.names ?? [];
  const canAdd = Boolean(query && !names.includes(query) && !value.includes(query));
  const hasOptions = canAdd || names.some((name) => !value.includes(name));

  useEffect(() => {
    const pending = request;
    const generation = ++pending.current;
    if (!open || disabled) return;
    setResults({ query, names: [], nextCursor: "", loading: true, error: "" });
    const timer = window.setTimeout(() => {
      void cli
        .listTags({ prefix: query })
        .response.then((reply) => {
          if (pending.current === generation) {
            setResults({ query, names: reply.tags.map((tag) => tag.name), nextCursor: reply.nextCursor, loading: false, error: "" });
          }
        })
        .catch((failure) => {
          if (pending.current === generation) {
            setResults({ query, names: [], nextCursor: "", loading: false, error: errorMessage(failure, "Could not search tags") });
          }
        });
    }, 200);
    return () => {
      ++pending.current;
      window.clearTimeout(timer);
    };
  }, [open, disabled, query, retry]);

  const loadMore = () => {
    if (!current?.nextCursor || current.loading) return;
    const generation = request.current;
    const cursor = current.nextCursor;
    inputRef.current?.focus();
    setResults({ ...current, loading: true, error: "" });
    void cli
      .listTags({ prefix: query, cursor })
      .response.then((reply) => {
        if (request.current === generation) {
          setResults((previous) =>
            previous?.query === query
              ? { query, names: [...previous.names, ...reply.tags.map((tag) => tag.name)], nextCursor: reply.nextCursor, loading: false, error: "" }
              : previous,
          );
        }
      })
      .catch((failure) => {
        if (request.current === generation) {
          setResults((previous) =>
            previous?.query === query ? { ...previous, loading: false, error: errorMessage(failure, "Could not search tags") } : previous,
          );
        }
      });
  };

  const footer =
    current?.nextCursor || current?.error ? (
      <>
        {current.error ? (
          <Feedback
            action={
              <Button
                size="small"
                onClick={
                  current.nextCursor
                    ? loadMore
                    : () => {
                        inputRef.current?.focus();
                        setOpen(true);
                        setRetry((value) => value + 1);
                      }
                }
                disabled={current.loading || disabled}
              >
                {current.nextCursor ? "Retry loading tags" : "Retry"}
              </Button>
            }
          >
            {current.error}
          </Feedback>
        ) : (
          <Button size="small" onClick={loadMore} disabled={current.loading || disabled}>
            Load more tags
          </Button>
        )}
      </>
    ) : undefined;

  return (
    <Box
      ref={root}
      sx={{ minWidth: 0 }}
      onBlur={(event) => {
        if (!root.current?.contains(event.relatedTarget)) setOpen(false);
      }}
      onKeyDown={(event) => {
        if (event.key !== "Escape" || event.target === inputRef.current) return;
        event.preventDefault();
        event.stopPropagation();
        setOpen(false);
        inputRef.current?.focus();
      }}
    >
      <Autocomplete
        multiple
        disablePortal
        size="small"
        freeSolo
        disableClearable
        filterSelectedOptions
        filterOptions={(values) => values}
        options={canAdd ? [query, ...names] : names}
        loading={Boolean(current?.loading)}
        loadingText="Searching tags…"
        noOptionsText={current?.error ? "Could not search tags" : "No matching tags"}
        value={value}
        inputValue={input}
        open={open && !disabled}
        disabled={disabled}
        onOpen={() => setOpen(true)}
        onClose={(event, reason) => {
          if (reason === "blur" && root.current?.contains((event as React.FocusEvent).relatedTarget)) return;
          setOpen(false);
        }}
        onChange={(_, tags) => {
          onChange(normalizeTags(tags));
          setInput("");
        }}
        onInputChange={(_, text, reason) => {
          if (reason === "input" || reason === "clear") {
            if (text.trim().toLowerCase() !== query) ++request.current;
            setInput(text);
          }
        }}
        renderOption={({ key, ...props }, option) => (
          <li key={key} {...props}>
            {canAdd && option === query ? `Add tag “${option}”` : option}
          </li>
        )}
        slots={{ paper: SearchPaper }}
        slotProps={{
          paper: { footer, onMouseDown: (event) => event.preventDefault() } as SearchPaperProps,
          popper: { popperOptions: { strategy: "fixed" } },
          chip: { size: "small", sx: { height: 26, color: "primary.main", bgcolor: "#e8f2fb" } },
        }}
        renderInput={(params) => (
          <TextField
            {...params}
            inputRef={inputRef}
            label="Tags"
            placeholder={value.length === 0 ? "Type a tag and press Enter" : "Add another tag"}
            error={error}
            helperText={helperText}
          />
        )}
      />
      {/* freeSolo omits an empty popup, so its recovery control must remain beside the field. */}
      {!hasOptions && footer}
    </Box>
  );
};

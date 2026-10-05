import { CircularProgress } from "@mui/material";

/**
 * The one placeholder for a files list with no rows to show: either it is still reading or the
 * read returned nothing. Pages pass it as `FileList`'s empty placeholder, so a pending read says
 * what it is doing instead of looking like an empty directory; publishing rows or a failure
 * replaces it.
 */
export const ListPlaceholder = ({ label, loading = false }: { label: string; loading?: boolean }) => (
  <div className="list-placeholder" role="status">
    {loading && <CircularProgress size={20} />}
    <span>{label}</span>
  </div>
);

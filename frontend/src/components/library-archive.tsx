import { Button } from "@mui/material";
import { Link } from "react-router";
import { FileSelection, FileScope } from "@/entity";

export const LibraryArchiveButton = ({
  fileIDs,
  label = "Back up selection",
  disabled = false,
  scope = FileScope.DEFAULT,
}: {
  fileIDs: bigint[];
  label?: string;
  disabled?: boolean;
  scope?: FileScope;
}) => (
  <Button
    component={Link}
    to="/backup"
    size="small"
    variant="contained"
    disabled={disabled}
    state={{
      selections: fileIDs.map((id) => ({
        selection: FileSelection.create({ target: { oneofKind: "library", library: { fileId: id } }, scope }),
        name: `File ${id}`,
        path: `File ${id}`,
      })),
    }}
  >
    {label}
  </Button>
);

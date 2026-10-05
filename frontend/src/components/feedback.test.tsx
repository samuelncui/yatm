import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { alertClasses, AlertTitle, Button } from "@mui/material";
import { describe, expect, it, vi } from "vitest";
import { FileBrowser, FileList } from "@samuelncui/chonky";
import { Feedback } from "./feedback";

describe("Inline feedback", () => {
  it("keeps application control styling inside the installed browser's theme", async () => {
    render(
      <FileBrowser files={[]} disableDragAndDrop>
        <FileList emptyPlaceholder={<Feedback action={<Button>Retry</Button>}>Could not read this directory</Feedback>} />
      </FileBrowser>,
    );
    expect(await screen.findByRole("button", { name: "Retry" })).toHaveStyle({ textTransform: "none" });
    expect(screen.getByRole("alert")).toHaveStyle({ borderRadius: "10px" });
  });

  it("keeps diagnostics accessible and delegates retry to the caller", async () => {
    const retry = vi.fn();
    render(
      <Feedback action={<Button onClick={retry}>Retry</Button>}>
        <AlertTitle>Could not load rows</AlertTitle>
        <details>
          <summary>Details</summary>
          <pre>Connection to the service failed</pre>
        </details>
      </Feedback>,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("Could not load rows");
    await userEvent.click(screen.getByText("Details"));
    expect(screen.getByText("Connection to the service failed")).toBeVisible();
    expect(retry).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(retry).toHaveBeenCalledOnce();
  });

  it("preserves the caller's pending control and supports messages without an action", async () => {
    const retry = vi.fn();
    const { rerender } = render(
      <Feedback
        action={
          <Button disabled onClick={retry}>
            Retry
          </Button>
        }
      >
        Could not load rows
      </Feedback>,
    );
    expect(screen.getByRole("button", { name: "Retry" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(retry).not.toHaveBeenCalled();
    rerender(<Feedback severity="warning">Find again to refresh this result</Feedback>);
    expect(screen.getByRole("alert")).toHaveTextContent("Find again to refresh this result");
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("does not reserve an empty action slot when the caller has no recovery action", () => {
    render(<Feedback action={false}>Could not add this item to the Restore list</Feedback>);
    expect(screen.getByRole("alert").querySelector(`.${alertClasses.action}`)).not.toBeInTheDocument();
  });
});

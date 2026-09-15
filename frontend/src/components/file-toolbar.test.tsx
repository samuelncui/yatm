import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { FileBrowser, FileToolbar, type ChonkyIconProps } from "@samuelncui/chonky";
import { describe, expect, it } from "vitest";

const Icon = ({ icon }: ChonkyIconProps) => <span data-testid={`icon-${icon}`} />;

describe("Shared file-list filter", () => {
  it("uses a filter icon before and after expansion without changing filter behavior", async () => {
    const user = userEvent.setup();
    render(
      <FileBrowser files={[]} disableDragAndDrop iconComponent={Icon}>
        <FileToolbar />
      </FileBrowser>,
    );
    const button = screen.getByRole("button", { name: "Filter" });
    expect(within(button).getByTestId("icon-filter")).toBeInTheDocument();
    expect(within(button).queryByTestId("icon-search")).not.toBeInTheDocument();
    await user.click(button);
    const field = screen.getByPlaceholderText("Filter");
    expect(screen.getByTestId("icon-filter")).toBeInTheDocument();
    await user.type(field, "alpha");
    expect(field).toHaveValue("alpha");
    await user.keyboard("{Escape}");
    expect(await screen.findByRole("button", { name: "Filter" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Filter" }));
    expect(screen.getByPlaceholderText("Filter")).toHaveValue("");
  });
});

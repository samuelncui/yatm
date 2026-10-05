import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ChonkyActions, FileToolbar, type ChonkyIconProps } from "@samuelncui/chonky";
import { describe, expect, it } from "vitest";
import "@/init";
import { GetDataUsageAction, RefreshListAction } from "@/actions";
import { FileBrowser } from "./file-browser";

const Icon = ({ icon }: ChonkyIconProps) => <span data-testid={`icon-${icon}`} />;

describe("Shared file-list filter", () => {
  it.each([undefined, [ChonkyActions.SortFilesByDate.id], true])("omits view switching with disabled actions %j", async (disabled) => {
    const user = userEvent.setup();
    render(
      <FileBrowser
        files={[]}
        disableDragAndDrop
        disableDefaultFileActions={disabled}
        fileActions={[ChonkyActions.EnableGridView, ChonkyActions.EnableListView, ChonkyActions.SortFilesByName]}
      >
        <FileToolbar />
      </FileBrowser>,
    );
    await user.click(screen.getByRole("button", { name: "Options" }));
    expect(screen.getByText("Sort by name")).toBeInTheDocument();
    expect(screen.queryByText(/Switch to/)).not.toBeInTheDocument();
    if (disabled) expect(screen.queryByText("Sort by date")).not.toBeInTheDocument();
    else expect(screen.getByText("Sort by date")).toBeInTheDocument();
  });

  it("uses matching rounded Material icons for refresh and data usage without visible labels", () => {
    render(
      <FileBrowser files={[]} fileActions={[RefreshListAction, GetDataUsageAction]} disableDragAndDrop>
        <FileToolbar />
      </FileBrowser>,
    );
    const refresh = screen.getByRole("button", { name: "Refresh" });
    const usage = screen.getByRole("button", { name: "Data Usage" });
    const refreshIcon = within(refresh).getByTestId("RefreshRoundedIcon");
    const usageIcon = within(usage).getByTestId("DataUsageRoundedIcon");
    expect(refreshIcon).toHaveAttribute("viewBox", "2 2 20 20");
    expect(usageIcon).toHaveAttribute("viewBox", "0 0 24 24");
    expect(refresh).not.toHaveTextContent("Refresh");
    expect(usage).not.toHaveTextContent("Data Usage");
    expect(refresh).toHaveAttribute("title", "Refresh");
    expect(usage).toHaveAttribute("title", "Data Usage");
  });

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

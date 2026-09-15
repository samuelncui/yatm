import { useState } from "react";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import RestoreTimePicker from "./restore-time-picker";
import { restoreCutoff } from "./restore-version-policy";

function ControlledPicker({ initial = "2026-09-01T12:30" }: { initial?: string }) {
  const [value, setValue] = useState(initial);
  const cutoff = restoreCutoff({ mode: "before", date: value });
  return (
    <>
      <RestoreTimePicker value={value} onChange={setValue} disabled={false} error={cutoff === undefined} />
      <output aria-label="Cutoff">{cutoff?.toString() ?? "Invalid"}</output>
    </>
  );
}

describe("Restore time picker", () => {
  it("opens an application calendar and cancels without changing the selected time", async () => {
    render(<ControlledPicker />);
    const user = userEvent.setup();
    const expected = String(new Date("2026-09-01T12:30").getTime());
    expect(screen.getByLabelText("Cutoff")).toHaveTextContent(expected);
    await user.click(screen.getByRole("button", { name: /Choose date/ }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("grid")).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByLabelText("Cutoff")).toHaveTextContent(expected);
  });

  it("edits a controlled local time and invalidates the cutoff when a date section is cleared", async () => {
    render(<ControlledPicker />);
    const user = userEvent.setup();
    const picker = screen.getByRole("group", { name: "Restore time" });
    await user.click(within(picker).getByRole("spinbutton", { name: "Year" }));
    await user.paste("2026-09-02 18:45");
    expect(screen.getByLabelText("Cutoff")).toHaveTextContent(String(new Date("2026-09-02T18:45").getTime()));
    await user.click(within(picker).getByRole("spinbutton", { name: "Day" }));
    await user.keyboard("{Backspace}");
    expect(screen.getByLabelText("Cutoff")).toHaveTextContent("Invalid");
  });

  it("does not retain a valid cutoff after an invalid date is entered", async () => {
    render(<ControlledPicker />);
    const user = userEvent.setup();
    const picker = screen.getByRole("group", { name: "Restore time" });
    await user.click(within(picker).getByRole("spinbutton", { name: "Year" }));
    await user.paste("1969-12-01 12:30");
    expect(screen.getByLabelText("Cutoff")).toHaveTextContent("Invalid");
    expect(screen.getByText("Choose a valid date and time.")).toBeVisible();
    await user.click(within(picker).getByRole("spinbutton", { name: "Year" }));
    await user.paste("2026-09-03 09:15");
    expect(screen.getByLabelText("Cutoff")).toHaveTextContent(String(new Date("2026-09-03T09:15").getTime()));
  });
});

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { FileBrowser, defineFileAction } from "@samuelncui/chonky";
import { describe, expect, it, vi } from "vitest";

const paste = defineFileAction({ id: "keyboard_paste", hotkeys: ["ctrl+v"], button: { name: "Paste", toolbar: true } });
const pressPaste = () => {
  const target = document.activeElement!;
  fireEvent.keyDown(target, { key: "Control", code: "ControlLeft", keyCode: 17, ctrlKey: true });
  fireEvent.keyDown(target, { key: "v", code: "KeyV", keyCode: 86, ctrlKey: true });
  fireEvent.keyUp(target, { key: "v", code: "KeyV", keyCode: 86, ctrlKey: true });
  fireEvent.keyUp(target, { key: "Control", code: "ControlLeft", keyCode: 17 });
};

describe("Shared Chonky keyboard ownership", () => {
  it("dispatches clipboard shortcuts only to the focused pane and preserves input editing", async () => {
    const left = vi.fn();
    const right = vi.fn();
    render(
      <>
        <FileBrowser instanceId="left" files={[]} disableDragAndDrop fileActions={[paste]} onFileAction={left}>
          <div data-testid="left-list">Left files</div>
          <input aria-label="File name" />
        </FileBrowser>
        <FileBrowser instanceId="right" files={[]} disableDragAndDrop fileActions={[paste]} onFileAction={right}>
          <div data-testid="right-list">Right files</div>
        </FileBrowser>
      </>,
    );
    fireEvent.pointerDown(screen.getByTestId("left-list"));
    pressPaste();
    await waitFor(() => expect(left.mock.calls.some(([data]) => data.id === paste.id)).toBe(true));
    expect(right.mock.calls.some(([data]) => data.id === paste.id)).toBe(false);
    left.mockClear();
    right.mockClear();
    fireEvent.pointerDown(screen.getByTestId("right-list"));
    pressPaste();
    await waitFor(() => expect(right.mock.calls.some(([data]) => data.id === paste.id)).toBe(true));
    expect(left.mock.calls.some(([data]) => data.id === paste.id)).toBe(false);
    right.mockClear();
    await userEvent.click(screen.getByRole("textbox", { name: "File name" }));
    pressPaste();
    expect(left.mock.calls.some(([data]) => data.id === paste.id)).toBe(false);
    expect(right.mock.calls.some(([data]) => data.id === paste.id)).toBe(false);
  });
});

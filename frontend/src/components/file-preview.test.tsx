import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { PreviewResource } from "@/entity";
import { PreviewMedia } from "./file-preview";

afterEach(() => vi.unstubAllGlobals());

const assets = [
  PreviewResource.create({ role: "poster", url: "poster.jpg" }),
  PreviewResource.create({ role: "timeline", url: "timeline.jpg", widthPx: 640, heightPx: 360 }),
  PreviewResource.create({ role: "timeline-map", url: "timeline.vtt" }),
];

it("steps through actual sampled frames with arrow keys while pointer positions still snap by time", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: true,
      text: async () => `WEBVTT

00:00:00.000 --> 00:00:10.000
timeline.jpg#xywh=0,0,320,180

00:00:10.000 --> 00:00:27.000
timeline.jpg#xywh=320,0,320,180

00:00:27.000 --> 00:01:00.000
timeline.jpg#xywh=0,180,320,180
`,
    }),
  );
  render(<PreviewMedia assets={assets} />);
  const slider = screen.getByRole("slider", { name: "Video preview position" });
  await waitFor(() => expect(slider).toBeEnabled());
  fireEvent.keyDown(slider, { key: "ArrowRight" });
  expect(slider).toHaveValue("10");
  expect(screen.getByRole("status")).toHaveTextContent("0:10 / 1:00");
  fireEvent.keyDown(slider, { key: "ArrowRight" });
  expect(slider).toHaveValue("27");
  fireEvent.keyDown(slider, { key: "ArrowRight" });
  expect(slider).toHaveValue("27");
  fireEvent.keyDown(slider, { key: "ArrowLeft" });
  expect(slider).toHaveValue("10");
  fireEvent.change(slider, { target: { value: "22" } });
  expect(slider).toHaveValue("27");
});

it.each([
  ["00:00:00.000", "00:00:59.999", "0:00 / 0:59"],
  ["00:00:00.000", "00:01:05.000", "0:00 / 1:05"],
  ["00:00:00.000", "00:59:59.999", "0:00 / 59:59"],
  ["00:00:59.999", "00:01:00.000", "0:59 / 1:00"],
  ["00:00:00.000", "01:00:00.000", "0:00:00 / 1:00:00"],
  ["00:00:00.000", "02:00:00.000", "0:00:00 / 2:00:00"],
  ["00:59:59.999", "01:00:00.000", "0:59:59 / 1:00:00"],
  ["01:00:00.000", "02:00:00.000", "1:00:00 / 2:00:00"],
  ["00:00:00.000", "23:59:59.999", "0:00:00 / 23:59:59"],
  ["00:00:00.000", "24:00:00.000", "0:00:00:00 / 1:00:00:00"],
  ["23:59:59.999", "24:00:00.000", "0:23:59:59 / 1:00:00:00"],
  ["24:00:00.000", "24:00:01.000", "1:00:00:00 / 1:00:00:01"],
  ["25:01:01.999", "51:04:05.000", "1:01:01:01 / 2:03:04:05"],
  ["2400:00:00.000", "2400:00:01.000", "100:00:00:00 / 100:00:00:01"],
])("formats Preview position %s with the fields required by duration %s", async (start, end, expected) => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: true,
      text: async () => `WEBVTT\n\n${start} --> ${end}\ntimeline.jpg#xywh=0,0,320,180\n`,
    }),
  );
  render(<PreviewMedia assets={assets} />);
  const slider = screen.getByRole("slider", { name: "Video preview position" });
  await waitFor(() => expect(slider).toBeEnabled());
  if (start === "00:00:00.000") expect(screen.getByRole("status").textContent).toBe(expected);
  fireEvent.keyDown(slider, { key: "ArrowRight" });
  expect(screen.getByRole("status").textContent).toBe(expected);
});

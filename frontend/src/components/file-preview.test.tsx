import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { PreviewResource } from "@/entity";
import { PreviewMedia } from "./file-preview";

afterEach(() => vi.unstubAllGlobals());

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
  render(
    <PreviewMedia
      assets={[
        PreviewResource.create({ role: "poster", url: "poster.jpg" }),
        PreviewResource.create({ role: "timeline", url: "timeline.jpg", widthPx: 640, heightPx: 360 }),
        PreviewResource.create({ role: "timeline-map", url: "timeline.vtt" }),
      ]}
    />,
  );
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

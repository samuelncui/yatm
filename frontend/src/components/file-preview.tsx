import { type MouseEvent, useEffect, useState } from "react";
import InsertDriveFileRoundedIcon from "@mui/icons-material/InsertDriveFileRounded";
import type { PreviewResource } from "@/entity";

type TimelineCue = {
  start: number;
  end: number;
  x: number;
  y: number;
  width: number;
  height: number;
};

export const PreviewMedia = ({ assets }: { assets: PreviewResource[] }) => {
  const thumbnail = assets.find((asset) => asset.role === "thumbnail");
  const poster = assets.find((asset) => asset.role === "poster");
  const timeline = assets.find((asset) => asset.role === "timeline");
  const timelineMap = assets.find((asset) => asset.role === "timeline-map");
  const [cues, setCues] = useState<TimelineCue[]>([]);
  const [previewCueIndex, setPreviewCueIndex] = useState<number | null>(null);
  const timelineMapURL = timelineMap?.url;

  useEffect(() => {
    if (!timelineMapURL) return;

    const controller = new AbortController();
    void fetch(timelineMapURL, { signal: controller.signal })
      .then((response) => {
        if (!response.ok) throw new Error(`Load Preview timeline failed: ${response.status}`);
        return response.text();
      })
      .then((text) => {
        setCues(parseTimeline(text));
      })
      .catch((error: unknown) => {
        if (!(error instanceof DOMException && error.name === "AbortError")) setCues([]);
      });
    return () => controller.abort();
  }, [timelineMapURL]);

  if (thumbnail) {
    return <img src={thumbnail.url} alt="" />;
  }
  if (!poster) {
    return <InsertDriveFileRoundedIcon />;
  }

  const duration = cues.at(-1)?.end ?? 0;
  const activeCue = previewCueIndex === null ? null : (cues[previewCueIndex] ?? null);
  const previewTime = activeCue?.start ?? 0;
  const snapToPreview = (time: number) => {
    if (cues.length === 0) return;

    let closestIndex = 0;
    for (let index = 1; index < cues.length; index += 1) {
      if (Math.abs(cues[index].start - time) < Math.abs(cues[closestIndex].start - time)) closestIndex = index;
    }
    setPreviewCueIndex(closestIndex);
  };
  const showTimeline = (event: MouseEvent<HTMLInputElement>) => {
    if (duration === 0) return;
    const bounds = event.currentTarget.getBoundingClientRect();
    const position = Math.min(Math.max((event.clientX - bounds.left) / bounds.width, 0), 1);
    snapToPreview(position * duration);
  };

  return (
    <div className="file-detail-video-preview">
      <div className="file-detail-video-frame">
        {activeCue && timeline ? <span style={timelineFrameStyle(timeline, activeCue)} /> : <img src={poster.url} alt="" />}
      </div>
      <div className="file-detail-video-controls">
        <input
          aria-label="Video preview position"
          type="range"
          min={0}
          max={duration}
          step="any"
          value={previewTime}
          disabled={duration === 0}
          onChange={(event) => snapToPreview(Number(event.currentTarget.value))}
          onKeyDown={(event) => {
            const previous = event.key === "ArrowLeft" || event.key === "ArrowDown";
            const next = event.key === "ArrowRight" || event.key === "ArrowUp";
            if (!previous && !next) return;
            event.preventDefault();
            setPreviewCueIndex((index) => Math.max(0, Math.min(cues.length - 1, (index ?? 0) + (previous ? -1 : 1))));
          }}
          onMouseMove={showTimeline}
        />
        <output>
          {formatPreviewTime(previewTime)} / {formatPreviewTime(duration)}
        </output>
      </div>
    </div>
  );
};

const timelineFrameStyle = (timeline: PreviewResource, cue: TimelineCue) => {
  const horizontal = timeline.widthPx > cue.width ? (cue.x / (timeline.widthPx - cue.width)) * 100 : 0;
  const vertical = timeline.heightPx > cue.height ? (cue.y / (timeline.heightPx - cue.height)) * 100 : 0;
  return {
    backgroundImage: `url(${timeline.url})`,
    backgroundSize: `${(timeline.widthPx / cue.width) * 100}% ${(timeline.heightPx / cue.height) * 100}%`,
    backgroundPosition: `${horizontal}% ${vertical}%`,
  };
};

const formatPreviewTime = (value: number) => {
  const seconds = Math.max(0, Math.floor(value));
  const minutes = Math.floor(seconds / 60);
  return `${minutes}:${String(seconds % 60).padStart(2, "0")}`;
};

const parseTimeline = (value: string): TimelineCue[] => {
  const time = (text: string) => {
    const [hours, minutes, seconds] = text.split(":");
    return Number(hours) * 3600 + Number(minutes) * 60 + Number(seconds);
  };
  const cues: TimelineCue[] = [];
  const blocks = value
    .trim()
    .split(/\n\s*\n/)
    .slice(1);
  for (const block of blocks) {
    const [range, location] = block.trim().split("\n");
    if (!range || !location) continue;
    const [start, end] = range.split(" --> ");
    const coordinates = location.match(/#xywh=(\d+),(\d+),(\d+),(\d+)$/);
    if (!start || !end || !coordinates) continue;
    cues.push({
      start: time(start),
      end: time(end),
      x: Number(coordinates[1]),
      y: Number(coordinates[2]),
      width: Number(coordinates[3]),
      height: Number(coordinates[4]),
    });
  }
  return cues;
};

package media

import (
	"context"
	"fmt"
	"math"
	"math/bits"
	"sort"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"google.golang.org/protobuf/proto"
)

// LTFSReadOptions carries label facts and an owned physical model, independently of native paths.
// Compression is the written volume-label policy, never the drive's current compression setting.
type LTFSReadOptions struct {
	BlockBytes     uint32
	Compression    *bool
	Partitions     map[string]uint32
	Model          *ReadCostModel
	Timing         *entity.TapeReadTiming
	Completion     *ReadCompletion
	DataEnd        *entity.TapeBytePosition
	PlanningBudget time.Duration
	ReadIndex      func(context.Context, func(*LTFSIndexEntry) error) error
}

type ltfsReadOrderProvider struct{ options LTFSReadOptions }

// WalkReadRanges provides current Index extents only when the planner needs missing locations.
func (p *ltfsReadOrderProvider) WalkReadRanges(ctx context.Context, yield func(readplan.RangeEntry) error) error {
	if p.options.ReadIndex == nil {
		return fmt.Errorf("current LTFS Index is unavailable")
	}
	return p.options.ReadIndex(ctx, func(entry *LTFSIndexEntry) error {
		ranges, _ := p.Ranges(ReadRequest{Path: entry.Path, Storage: entry.Storage})
		return yield(readplan.RangeEntry{Path: entry.Path, Size: entry.Size, Ranges: ranges})
	})
}

// NewLTFSReadOrderProvider prices the mounted LTFS reader's last-block cache and ordinary seeks.
// Missing facts disable optional planning without changing native source access.
func NewLTFSReadOrderProvider(options LTFSReadOptions) (ReadOrderProvider, string) {
	// A current drive control cannot certify the compression history needed by interpolation.
	if options.Compression == nil || *options.Compression {
		return nil, "LTFS volume-label compression is enabled or unknown"
	}
	if options.BlockBytes == 0 || len(options.Partitions) == 0 || !options.Model.Valid() {
		return nil, "LTFS label block size, physical partition mapping or geometry is unavailable"
	}
	if options.Timing == nil || options.Timing.BytesPerSecond == nil || options.Timing.GetBytesPerSecond() <= 0 ||
		options.Timing.PostLocateReadOverheadSeconds == nil {
		return nil, "LTFS read timing is incomplete"
	}
	for _, value := range []float64{options.Timing.GetBytesPerSecond(), options.Timing.GetPostLocateReadOverheadSeconds()} {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, "LTFS read timing is invalid"
		}
	}

	// Actual Data EOD bounds complete reads, independently of the previous Index's start block.
	if options.DataEnd == nil || options.DataEnd.OffsetBytes != 0 {
		return nil, "LTFS Data partition EOD is unavailable"
	}
	intervals := options.Model.intervals[options.DataEnd.Partition]
	if len(intervals) == 0 || intervals[len(intervals)-1].high != options.DataEnd.LogicalObject {
		return nil, "LTFS Data partition EOD does not match the position map"
	}
	if options.Completion == nil || options.Completion.FullSpanSeconds >= float64(math.MaxInt64)/float64(time.Second) {
		return nil, "LTFS completion timing is unavailable or unrepresentable"
	}
	if _, known := options.Model.UnloadCost(options.DataEnd, options.Completion); !known {
		return nil, "LTFS completion timing or physical endpoint is unavailable"
	}

	// The planning snapshot never borrows mutable settings or the caller's partition containers.
	partitions := make(map[string]uint32, len(options.Partitions))
	dataPartition := false
	for symbol, partition := range options.Partitions {
		if len(symbol) != 1 {
			return nil, "LTFS symbolic partition mapping is invalid"
		}
		partitions[symbol] = partition
		dataPartition = dataPartition || partition == options.DataEnd.Partition
	}
	if !dataPartition {
		return nil, "LTFS Data partition EOD has no label mapping"
	}
	options.Partitions = partitions
	options.Compression = proto.Bool(false)
	options.Timing = proto.Clone(options.Timing).(*entity.TapeReadTiming)
	completion := *options.Completion
	options.Completion = &completion
	options.DataEnd = proto.Clone(options.DataEnd).(*entity.TapeBytePosition)
	return &ltfsReadOrderProvider{options: options}, "LTFS route comparison assumes no Index rewrite and includes rewind/unload; common initial access is unquoted"
}

func (p *ltfsReadOrderProvider) ReadPlanningBudget() time.Duration { return p.options.PlanningBudget }

func (p *ltfsReadOrderProvider) Current(ctx context.Context) (readplan.State, bool, error) {
	// Neither the captured Index location nor a pre-mount observation describes the mounted cursor.
	return readplan.State{}, false, ctx.Err()
}

func (p *ltfsReadOrderProvider) Ranges(request ReadRequest) ([]readplan.Range, bool) {
	// Native LTFS reads visit extents in file-offset order, including fragmented and sparse files.
	metadata := request.Storage.GetMetadata().GetLtfs()
	if !p.options.Model.Valid() || len(metadata.GetExtents()) == 0 || len(metadata.Extents) > readplan.MaxRanges {
		return nil, false
	}
	extents := append([]*entity.LtfsExtent(nil), metadata.Extents...)
	for _, extent := range extents {
		if extent == nil {
			return nil, false
		}
	}
	sort.SliceStable(extents, func(i, j int) bool { return extents[i].FileOffsetBytes < extents[j].FileOffsetBytes })
	ranges := make([]readplan.Range, 0, len(extents))
	fileEnd, blockBytes := uint64(0), uint64(p.options.BlockBytes)
	for _, extent := range extents {
		partition, known := p.options.Partitions[extent.Partition]
		if !known || partition != p.options.DataEnd.Partition || extent.ByteCount == 0 || extent.ByteOffset >= blockBytes || extent.FileOffsetBytes < fileEnd {
			return nil, false
		}
		end, carry := bits.Add64(extent.FileOffsetBytes, extent.ByteCount, 0)
		if carry != 0 {
			return nil, false
		}
		fileEnd = end
		bytes, carry := bits.Add64(extent.ByteOffset, extent.ByteCount, 0)
		if carry != 0 {
			return nil, false
		}
		end, carry = bits.Add64(extent.StartBlock, bytes/blockBytes, 0)
		if carry != 0 {
			return nil, false
		}
		interval := readplan.Range{
			Start:  &entity.TapeBytePosition{Partition: partition, LogicalObject: extent.StartBlock, OffsetBytes: uint32(extent.ByteOffset)},
			End:    &entity.TapeBytePosition{Partition: partition, LogicalObject: end, OffsetBytes: uint32(bytes % blockBytes)},
			Stream: "ltfs",
		}
		if !p.covers(interval) {
			return nil, false
		}
		ranges = append(ranges, interval)
	}
	return ranges, true
}

// FixedFirstReadState forecasts the complete common first file; it is not an observed cursor.
func (p *ltfsReadOrderProvider) FixedFirstReadState(span readplan.Span) (readplan.State, bool) {
	return p.CanonicalReadState(span)
}

// CanonicalReadState follows the LTFS volume-wide last-block cache after a complete file read.
func (p *ltfsReadOrderProvider) CanonicalReadState(span readplan.Span) (readplan.State, bool) {
	if len(span.Ranges) == 0 || len(span.Ranges) > readplan.MaxRanges {
		return readplan.State{}, false
	}
	for _, interval := range span.Ranges {
		if !p.covers(interval) {
			return readplan.State{}, false
		}
	}
	return ltfsRangeEnd(span.Ranges[len(span.Ranges)-1]), true
}

func ltfsRangeEnd(interval readplan.Range) readplan.State {
	end := interval.End.LogicalObject
	if interval.End.OffsetBytes != 0 {
		end++
	}
	positions := &[2]entity.TapeBytePosition{
		{Partition: interval.End.Partition, LogicalObject: end},
		{Partition: interval.End.Partition, LogicalObject: end - 1},
	}
	return readplan.State{Position: &positions[0], Cache: &positions[1], Stream: "ltfs"}
}

func (p *ltfsReadOrderProvider) covers(interval readplan.Range) bool {
	// Partial records retain their exact byte anchors while geometry covers every consumed block.
	if !p.options.Model.Valid() || interval.Stream != "ltfs" || interval.Start == nil || interval.End == nil ||
		interval.Start.Partition != p.options.DataEnd.Partition || interval.Start.Partition != interval.End.Partition || interval.Start.OffsetBytes >= p.options.BlockBytes ||
		interval.End.OffsetBytes >= p.options.BlockBytes || comparePosition(interval.Start, interval.End) >= 0 ||
		interval.End.LogicalObject == math.MaxUint64 && interval.End.OffsetBytes != 0 {
		return false
	}
	end := ltfsRangeEnd(interval).Position.LogicalObject
	wraps := p.options.Model.intervals[interval.Start.Partition]
	at := sort.Search(len(wraps), func(i int) bool { return wraps[i].high > interval.Start.LogicalObject })
	if at == len(wraps) || interval.Start.LogicalObject < wraps[at].low {
		return false
	}
	covered := wraps[at].high
	for covered < end && at+1 < len(wraps) {
		at++
		if wraps[at].low != covered {
			return false
		}
		covered = wraps[at].high
	}
	return end <= covered
}

func (p *ltfsReadOrderProvider) Evaluate(from readplan.State, interval readplan.Range) (readplan.Transition, bool) {
	// Only LTFS's demonstrated last-block cache and adjacent READ avoid its ordinary seek path.
	if !p.covers(interval) || from.Position == nil || from.Position.OffsetBytes != 0 ||
		from.Position.Partition != interval.Start.Partition {
		return readplan.Transition{}, false
	}
	start, end := interval.Start.LogicalObject, ltfsRangeEnd(interval)
	blocks := end.Position.LogicalObject - start
	method, cost := readplan.MethodSeek, time.Duration(0)
	switch {
	case from.Stream == "ltfs" && from.Cache != nil && from.Cache.Partition == interval.Start.Partition &&
		from.Cache.OffsetBytes == 0 && from.Cache.LogicalObject == start && start != math.MaxUint64 &&
		from.Position.LogicalObject == start+1:
		method, blocks = readplan.MethodCache, blocks-1
	case from.Position.LogicalObject == start:
		method = readplan.MethodContinue
	default:
		setup, known := p.options.Model.AccessCost(from.Position, ltfsContainingBlock(interval.Start))
		if !known {
			return readplan.Transition{}, false
		}
		overhead, known := readSeconds(p.options.Timing.GetPostLocateReadOverheadSeconds())
		if !known {
			return readplan.Transition{}, false
		}
		cost = setup + overhead
	}

	// Every extent is priced in whole blocks, including a final short record.
	if blocks != 0 {
		high, bytes := bits.Mul64(blocks, uint64(p.options.BlockBytes))
		if high != 0 {
			return readplan.Transition{}, false
		}
		duration, known := readSeconds(float64(bytes) / p.options.Timing.GetBytesPerSecond())
		if !known {
			return readplan.Transition{}, false
		}
		cost += duration
	}
	return readplan.Transition{Cost: cost, Next: end, Method: method}, true
}

// Finish quotes rewind/unload under the no-Index-rewrite assumption used for LTFS reads.
// This is model policy, not an observation that the mounted volume is read-only or clean.
func (p *ltfsReadOrderProvider) Finish(from readplan.State) (time.Duration, bool) {
	// The forecast exit remains in the qualified Data partition and keeps its physical wrap scale.
	if from.Position == nil || from.Position.Partition != p.options.DataEnd.Partition {
		return 0, false
	}
	return p.options.Model.UnloadCost(from.Position, p.options.Completion)
}

func ltfsContainingBlock(position *entity.TapeBytePosition) *entity.TapeBytePosition {
	if position == nil {
		return nil
	}
	return &entity.TapeBytePosition{Partition: position.Partition, LogicalObject: position.LogicalObject}
}

func (p *ltfsReadOrderProvider) ReadIndex(position *entity.TapeBytePosition) (readplan.Index, bool) {
	return p.options.Model.ReadIndex(ltfsContainingBlock(position))
}

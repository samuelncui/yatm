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
	Completion     *ReadCompletion
	DataEnd        *entity.TapeBytePosition
	PlanningBudget time.Duration
	ReadIndex      func(context.Context, func(*LTFSIndexEntry) error) error
}

type ltfsReadOrderProvider struct {
	options LTFSReadOptions
	// origin is the assumed post-mount position, or nil when the position map cannot resolve it.
	origin *entity.TapeBytePosition
}

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
	// Actual Data EOD bounds complete reads, independently of the previous Index's start block.
	if options.DataEnd == nil || options.DataEnd.OffsetBytes != 0 {
		return nil, "LTFS Data partition EOD is unavailable"
	}
	intervals := options.Model.intervals[options.DataEnd.Partition]
	if len(intervals) == 0 || intervals[len(intervals)-1].high != options.DataEnd.LogicalObject {
		return nil, "LTFS Data partition EOD does not match the position map"
	}
	if options.Completion == nil || options.Completion.FullSpanSeconds >= float64(math.MaxInt64)/float64(time.Second) ||
		!(options.Completion.LTFSUnmountSeconds >= 0) || options.Completion.LTFSUnmountSeconds >= float64(math.MaxInt64)/float64(time.Second) {
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
	completion := *options.Completion
	options.Completion = &completion
	options.DataEnd = proto.Clone(options.DataEnd).(*entity.TapeBytePosition)

	// Mount reads the Index near BOT, so the first file is priced from the Data partition's first
	// object when the map resolves it; otherwise the common first file stays fixed and unquoted.
	provider := &ltfsReadOrderProvider{options: options}
	origin := &entity.TapeBytePosition{Partition: options.DataEnd.Partition}
	if _, known := options.Model.Point(origin); !known {
		return provider, "LTFS route comparison assumes no Index rewrite and includes rewind/unload; the post-mount position is unresolved, so common initial access is unquoted"
	}
	provider.origin = origin
	return provider, "LTFS route comparison assumes no Index rewrite and includes rewind/unload; initial access is priced from the Data partition start"
}

func (p *ltfsReadOrderProvider) ReadPlanningBudget() time.Duration { return p.options.PlanningBudget }

func (p *ltfsReadOrderProvider) MinimumGain() time.Duration { return p.options.Model.MinimumGain() }

// Current returns the assumed post-mount position, not an observed cursor: mount reads the Index
// from the Index partition near BOT, which leaves the Data partition's first object closest.
func (p *ltfsReadOrderProvider) Current(ctx context.Context) (readplan.State, bool, error) {
	if p.origin == nil {
		return readplan.State{}, false, ctx.Err()
	}
	return readplan.State{Position: &entity.TapeBytePosition{Partition: p.origin.Partition,
		LogicalObject: p.origin.LogicalObject}, Stream: "ltfs"}, true, ctx.Err()
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
		interval.End.OffsetBytes >= p.options.BlockBytes || entity.CompareTapeBytePosition(interval.Start, interval.End) >= 0 ||
		interval.End.LogicalObject == math.MaxUint64 && interval.End.OffsetBytes != 0 {
		return false
	}
	return p.options.Model.covers(interval.Start.Partition, interval.Start.LogicalObject, ltfsRangeEnd(interval).Position.LogicalObject)
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
		// Only a completed read leaves a cached block and fills the read-ahead buffer; the post-mount
		// origin has neither, because mount leaves the head on the Index partition.
		access := p.options.Model.AccessCost
		if from.Cache == nil {
			access = p.options.Model.UnbufferedAccessCost
		}
		setup, known := access(from.Position, ltfsContainingBlock(interval.Start))
		if !known {
			return readplan.Transition{}, false
		}
		cost = setup
	}

	// Every extent is priced in whole blocks, including a final short record, with each wrap turn up
	// to the host-next block.
	if blocks != 0 {
		first := &entity.TapeBytePosition{Partition: end.Cache.Partition, LogicalObject: end.Position.LogicalObject - blocks}
		seconds, known := p.options.Model.ReadingSeconds(first, end.Position, blocks)
		if !known {
			return readplan.Transition{}, false
		}
		duration, known := readSeconds(seconds)
		if !known {
			return readplan.Transition{}, false
		}
		cost += duration
	}
	return readplan.Transition{Cost: cost, Next: end, Method: method}, true
}

// Finish quotes rewind/unload plus the LTFS unmount time under the no-Index-rewrite assumption used
// for LTFS reads.
// This is model policy, not an observation that the mounted volume is read-only or clean.
func (p *ltfsReadOrderProvider) Finish(from readplan.State) (time.Duration, bool) {
	// The forecast exit remains in the qualified Data partition and keeps its physical wrap scale.
	if from.Position == nil || from.Position.Partition != p.options.DataEnd.Partition {
		return 0, false
	}
	unload, known := p.options.Model.UnloadCost(from.Position, p.options.Completion)
	if !known {
		return 0, false
	}
	unmount, known := readSeconds(p.options.Completion.LTFSUnmountSeconds)
	if !known || unload > math.MaxInt64-unmount {
		return 0, false
	}
	return unload + unmount, true
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

package library

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// UpdateMediaTapePositionMap replaces only derived geometry after rechecking its current owner.
// expected carries the previously read map for compare-and-swap; nil value invalidates that map.
// APPEND callers retain the complete prefix when constructing their replacement.
func (l *Library) UpdateMediaTapePositionMap(
	ctx context.Context,
	expected *Media,
	value *entity.TapePositionMap,
) (*Media, error) {
	// Identity and structural errors cannot enter the catalog mutation boundary.
	if expected == nil || expected.ID <= 0 || expected.Kind != entity.MediaKind_MEDIA_KIND_TAPE ||
		expected.Profile.GetTape() == nil {
		return nil, fmt.Errorf("update Media Tape position map failed, Media identity is missing")
	}
	if err := ValidateTapePositionMap(expected, value); err != nil {
		return nil, err
	}

	// Re-read on the writer so an import, invalidation or competing observation cannot be overwritten.
	var updated *Media
	if err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		stored := new(Media)
		if err := tx.First(stored, expected.ID).Error; err != nil {
			return fmt.Errorf("read Media for Tape position map update failed, %w", err)
		}
		if stored.Kind != expected.Kind || stored.Identity != expected.Identity ||
			!proto.Equal(stored.Profile, expected.Profile) {
			return fmt.Errorf("Media identity changed, media_id=%d", expected.ID)
		}
		if !proto.Equal(stored.TapePositionMap, expected.TapePositionMap) {
			return fmt.Errorf("Media Tape position map changed, media_id=%d", expected.ID)
		}
		if proto.Equal(stored.TapePositionMap, value) {
			updated = stored
			return nil
		}

		// A request-owned model preserves the ordinary GORM mutation hooks without changing the profile.
		if value != nil {
			value = proto.Clone(value).(*entity.TapePositionMap)
		}
		if err := tx.Model(stored).Update("tape_position_map", value).Error; err != nil {
			return fmt.Errorf("update Media Tape position map failed, media_id=%d, %w", expected.ID, err)
		}
		stored.TapePositionMap = value
		updated = stored
		return nil
	}); err != nil {
		return nil, err
	}
	return updated, nil
}

// ValidateTapePositionMap validates initialization binding and declared logical coverage.
// It does not certify a map's physical accuracy or replace current-session identity checks.
func ValidateTapePositionMap(media *Media, value *entity.TapePositionMap) error {
	// Absence is valid for every backend; present geometry needs a Tape initialization owner.
	if value == nil {
		return nil
	}
	if media == nil || media.Kind != entity.MediaKind_MEDIA_KIND_TAPE || media.Profile.GetTape() == nil {
		return fmt.Errorf("Tape position map requires Tape Media")
	}
	tape := media.Profile.GetTape()
	if tape.Format == TapeFormatTarPAXV1 {
		if value.InitializationId == "" || value.InitializationId != tape.GetTar().GetInitializationId() ||
			value.LtfsUuid != "" || value.LtfsIndexGeneration != nil {
			return fmt.Errorf("Tape position map tar initialization does not match Media")
		}
	} else if tape.Format == TapeFormatLTFSV0 || tape.Format == TapeFormatLTFSV1 {
		if _, err := uuid.Parse(value.LtfsUuid); err != nil {
			return fmt.Errorf("Tape position map LTFS UUID is invalid, %w", err)
		}
		if value.InitializationId != "" || value.LtfsIndexGeneration == nil || *value.LtfsIndexGeneration == 0 {
			return fmt.Errorf("Tape position map requires an LTFS index generation")
		}
	} else {
		return fmt.Errorf("Tape position map format is unsupported, format=%q", tape.Format)
	}

	// Unknown coordinate scale stays absent; an endpoint interpretation and source are always explicit.
	if strings.TrimSpace(value.Source) == "" ||
		(value.EndpointConvention != entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE &&
			value.EndpointConvention != entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE) {
		return fmt.Errorf("Tape position map requires source and endpoint convention")
	}
	if (value.MinLpos == nil) != (value.MaxLpos == nil) ||
		value.MinLpos != nil && *value.MaxLpos <= *value.MinLpos {
		return fmt.Errorf("Tape position map LPOS scale is invalid")
	}
	if len(value.Partitions) == 0 || len(value.Wraps) == 0 {
		return fmt.Errorf("Tape position map has no partition traversal")
	}

	// Partition anchors and actual EOD bound every declared endpoint without inventing coverage.
	partitions := make(map[uint32]*entity.TapePositionPartition, len(value.Partitions))
	for _, partition := range value.Partitions {
		if partition == nil || partitions[partition.Partition] != nil {
			return fmt.Errorf("Tape position map has a missing or duplicate partition")
		}
		if partition.Eod != nil && *partition.Eod < partition.Bot {
			return fmt.Errorf("Tape position map EOD precedes BOT")
		}
		if tape.Format == TapeFormatTarPAXV1 && (partition.Partition != 0 || partition.Bot != 0) {
			return fmt.Errorf("tar Tape position map requires partition-zero BOT")
		}
		partitions[partition.Partition] = partition
	}
	return validateTapePositionWraps(value, partitions)
}

func validateTapePositionWraps(
	value *entity.TapePositionMap,
	partitions map[uint32]*entity.TapePositionPartition,
) error {
	// Remember final descriptors before validating partial tails and preserve the supplied traversal order.
	last := make(map[uint32]int, len(partitions))
	for i, wrap := range value.Wraps {
		if wrap == nil {
			return fmt.Errorf("Tape position map contains a missing wrap")
		}
		last[wrap.Partition] = i
	}
	if len(last) != len(partitions) {
		return fmt.Errorf("Tape position map partition has no traversal")
	}

	// A missing endpoint withdraws its successor's lower bound instead of bridging that gap.
	seen := make(map[[2]uint32]bool, len(value.Wraps))
	previous := make(map[uint32]*entity.TapePositionWrap, len(partitions))
	endpoints := make(map[uint32]uint64, len(partitions))
	for i, wrap := range value.Wraps {
		partition := partitions[wrap.Partition]
		key := [2]uint32{wrap.Partition, wrap.Wrap}
		if partition == nil || seen[key] ||
			(wrap.Direction != entity.TapePositionDirection_TAPE_POSITION_DIRECTION_FORWARD &&
				wrap.Direction != entity.TapePositionDirection_TAPE_POSITION_DIRECTION_REVERSE) {
			return fmt.Errorf("Tape position map has an invalid partition, duplicate wrap or direction")
		}
		seen[key] = true
		low, known := partition.Bot, true
		if before := previous[wrap.Partition]; before != nil {
			low, known = tapePositionWrapEnd(value, before)
		}
		if wrap.Endpoint == nil {
			if wrap.EstimatedSpanObjects != nil || wrap.EndpointUncertaintyObjects != nil {
				return fmt.Errorf("Tape position map scale or uncertainty has no endpoint")
			}
			previous[wrap.Partition] = wrap
			continue
		}
		high, _ := tapePositionWrapEnd(value, wrap)
		if !wrap.Full {
			high = *wrap.Endpoint
		}
		if wrap.Full && value.EndpointConvention == entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE &&
			*wrap.Endpoint == math.MaxUint64 {
			return fmt.Errorf("Tape position map inclusive endpoint overflows")
		}
		if high < partition.Bot || known && high <= low || partition.Eod != nil && high > *partition.Eod {
			return fmt.Errorf("Tape position map endpoint is outside logical coverage")
		}
		if before, exists := endpoints[wrap.Partition]; exists && high < before {
			return fmt.Errorf("Tape position map endpoints contradict traversal")
		}
		if !wrap.Full && (last[wrap.Partition] != i || partition.Eod == nil || high != *partition.Eod) {
			return fmt.Errorf("Tape position map partial wrap must end at actual EOD")
		}
		if wrap.EstimatedSpanObjects != nil && (wrap.Full || !known || *wrap.EstimatedSpanObjects == 0 ||
			*wrap.EstimatedSpanObjects < high-low) {
			return fmt.Errorf("Tape position map partial scale is shorter than coverage")
		}
		endpoints[wrap.Partition], previous[wrap.Partition] = high, wrap
	}
	return nil
}

func tapePositionWrapEnd(value *entity.TapePositionMap, wrap *entity.TapePositionWrap) (uint64, bool) {
	if !wrap.Full || wrap.Endpoint == nil {
		return 0, false
	}
	if value.EndpointConvention == entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE {
		if *wrap.Endpoint == math.MaxUint64 {
			return 0, false
		}
		return *wrap.Endpoint + 1, true
	}
	return *wrap.Endpoint, true
}

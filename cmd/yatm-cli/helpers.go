package main

import (
	"fmt"
	"strings"

	"github.com/samuelncui/yatm/entity"
)

func positiveID(name string, value int64) error {
	if value <= 0 {
		return usageError(fmt.Errorf("%s must be positive, value=%d", name, value))
	}
	return nil
}

func positiveIDs(name string, values []int64) ([]int64, error) {
	if len(values) == 0 {
		return nil, usageError(fmt.Errorf("at least one %s is required", name))
	}
	return optionalPositiveIDs(name, values)
}

// optionalPositiveIDs validates and deduplicates identifiers whose scope supplies a default.
func optionalPositiveIDs(name string, values []int64) ([]int64, error) {
	// Validate and deduplicate caller-provided identifiers in their original order.
	found := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if err := positiveID(name, value); err != nil {
			return nil, err
		}
		if _, ok := found[value]; ok {
			continue
		}
		found[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func validateLimit(name string, value *int64) error {
	if value != nil && *value <= 0 {
		return usageError(fmt.Errorf("%s must be positive, value=%d", name, *value))
	}
	return nil
}

func validateLimit32(name string, value *int32) error {
	if value != nil && *value <= 0 {
		return usageError(fmt.Errorf("%s must be positive, value=%d", name, *value))
	}
	return nil
}

func validateOffset(name string, value *int64) error {
	if value != nil && *value < 0 {
		return usageError(fmt.Errorf("%s must not be negative, value=%d", name, *value))
	}
	return nil
}

// jobResultPageOptions are the shared page flags of every Job result listing.
type jobResultPageOptions struct {
	Cursor       string `long:"cursor" description:"Order key cursor taken from a previous page row"`
	Order        string `long:"order" choice:"asc" choice:"desc" default:"asc" description:"Page direction; desc pages backward from the cursor"`
	Offset       *int64 `long:"offset" description:"Row offset used to anchor a jump instead of a cursor"`
	IncludeTotal bool   `long:"include-total" description:"Include the filtered result-set total"`
	Limit        *int32 `long:"limit" description:"Maximum results in this page"`
}

// jobResultPageLimit mirrors the server bound so an oversized page fails before transport.
const jobResultPageLimit = 1000

func (o jobResultPageOptions) validate() error {
	if o.Limit != nil && (*o.Limit <= 0 || *o.Limit > jobResultPageLimit) {
		return usageError(fmt.Errorf("limit must be between 1 and %d", jobResultPageLimit))
	}
	return validateOffset("Job result offset", o.Offset)
}

func (o jobResultPageOptions) order() entity.JobResultOrder {
	if o.Order == "desc" {
		return entity.JobResultOrder_JOB_RESULT_ORDER_DESCENDING
	}
	return entity.JobResultOrder_JOB_RESULT_ORDER_ASCENDING
}

// pageLimit leaves zero to the server default.
func (o jobResultPageOptions) pageLimit() int32 {
	if o.Limit == nil {
		return 0
	}
	return *o.Limit
}

func parseMediaKinds(values []string) ([]entity.MediaKind, error) {
	result := make([]entity.MediaKind, 0, len(values))
	for _, value := range values {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "tape":
			result = append(result, entity.MediaKind_MEDIA_KIND_TAPE)
		case "volume":
			result = append(result, entity.MediaKind_MEDIA_KIND_VOLUME)
		default:
			return nil, usageError(fmt.Errorf("unsupported Media kind, kind=%q", value))
		}
	}
	return result, nil
}

func parseVolumeType(value string) (entity.VolumeType, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "hdd":
		return entity.VolumeType_VOLUME_TYPE_HDD, nil
	case "hm-smr":
		return entity.VolumeType_VOLUME_TYPE_HM_SMR, nil
	default:
		return entity.VolumeType_VOLUME_TYPE_UNSPECIFIED,
			usageError(fmt.Errorf("unsupported Volume type, type=%q", value))
	}
}

func parseCopyStatuses(values []string) ([]entity.CopyStatus, error) {
	result := make([]entity.CopyStatus, 0, len(values))
	for _, value := range values {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "pending":
			result = append(result, entity.CopyStatus_COPY_STATUS_PENDING)
		case "staged":
			result = append(result, entity.CopyStatus_COPY_STATUS_STAGED)
		case "submitted":
			result = append(result, entity.CopyStatus_COPY_STATUS_SUBMITTED)
		case "completed":
			result = append(result, entity.CopyStatus_COPY_STATUS_COMPLETED)
		default:
			return nil, usageError(fmt.Errorf("unsupported copy status, status=%q", value))
		}
	}
	return result, nil
}

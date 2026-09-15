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
			result = append(result, entity.CopyStatus_PENDING)
		case "staged":
			result = append(result, entity.CopyStatus_STAGED)
		case "submitted":
			result = append(result, entity.CopyStatus_SUBMITTED)
		case "completed":
			result = append(result, entity.CopyStatus_COMPLETED)
		default:
			return nil, usageError(fmt.Errorf("unsupported copy status, status=%q", value))
		}
	}
	return result, nil
}

package main

import (
	"fmt"
	"time"

	"github.com/samuelncui/yatm/entity"
)

type restorePolicyOptions struct {
	Before                string `long:"before" value-name:"RFC3339" description:"Latest recorded backup at or before this time; explicit version IDs override it"`
	SkipUnmatchedVersions bool   `long:"skip-unmatched-versions" description:"Explicitly skip automatic selections without a version matching --before"`
}

func (o restorePolicyOptions) policy(restore bool) (*entity.RestoreVersionPolicy, error) {
	// Keep Restore-only selection controls out of original-content inspection.
	if !restore && (o.Before != "" || o.SkipUnmatchedVersions) {
		return nil, usageError(fmt.Errorf("before and skip-unmatched-versions require restore"))
	}
	if o.Before == "" {
		if o.SkipUnmatchedVersions {
			return nil, usageError(fmt.Errorf("skip-unmatched-versions requires before"))
		}
		return nil, nil
	}

	// Require an explicit timezone so scripts do not depend on client/server locales.
	value, err := time.Parse(time.RFC3339Nano, o.Before)
	if err != nil {
		return nil, usageError(fmt.Errorf("before must be RFC3339 with a timezone: %w", err))
	}
	stamp := value.UnixMilli()
	if stamp < 0 {
		return nil, usageError(fmt.Errorf("before must not precede the Unix epoch"))
	}
	return &entity.RestoreVersionPolicy{BeforeAtMs: &stamp}, nil
}

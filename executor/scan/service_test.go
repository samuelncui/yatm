package scan

import (
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestScanSelectionAndPolicyValidation(t *testing.T) {
	location := func(path string) *entity.FileSelection {
		return &entity.FileSelection{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: 1, Path: path}}}
	}
	for _, test := range []struct {
		name string
		spec *entity.ScanJobSpec
		bad  bool
	}{
		{name: "whole Location", spec: &entity.ScanJobSpec{LocationId: 1}},
		{name: "explicit root", spec: &entity.ScanJobSpec{LocationId: 1, Paths: []string{""}}},
		{name: "selected root", spec: &entity.ScanJobSpec{Selections: []*entity.FileSelection{location("")}}},
		{name: "relative file", spec: &entity.ScanJobSpec{Selections: []*entity.FileSelection{location("folder/file")}}},
		{name: "escape", spec: &entity.ScanJobSpec{Selections: []*entity.FileSelection{location("../file")}}, bad: true},
		{name: "negative source", spec: &entity.ScanJobSpec{LocationId: 1, MediaId: -1}, bad: true},
		{name: "mixed sources", spec: &entity.ScanJobSpec{LocationId: 1, MediaId: 1}, bad: true},
		{name: "check cached", spec: &entity.ScanJobSpec{MediaId: 1, ResultPolicy: entity.ScanResultPolicy_VERIFY_COPIES, SignaturePolicy: entity.ScanSignaturePolicy_KNOWN_ONLY}, bad: true},
		{name: "check read", spec: &entity.ScanJobSpec{MediaId: 1, ResultPolicy: entity.ScanResultPolicy_VERIFY_COPIES, SignaturePolicy: entity.ScanSignaturePolicy_FORCE_READ}},
		{name: "original inventory", spec: &entity.ScanJobSpec{LocationId: 1, ResultPolicy: entity.ScanResultPolicy_PUBLISH_INVENTORY}, bad: true},
		{name: "invalid policy", spec: &entity.ScanJobSpec{LocationId: 1, SignaturePolicy: -1}, bad: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateSpec(test.spec)
			if test.bad {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

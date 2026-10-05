package scan

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestScanCreateRejectsMissingInput(t *testing.T) {
	for _, test := range []struct {
		name    string
		request *entity.CreateScanJobRequest
	}{
		{name: "request"},
		{name: "specification", request: &entity.CreateScanJobRequest{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Malformed requests must fail before touching an Executor or creating a Job.
			require.NotPanics(t, func() {
				reply, err := (&service{}).Create(context.Background(), test.request)
				require.ErrorContains(t, err, "missing")
				require.Nil(t, reply)
			})
		})
	}
}

func scanLocationSelections(id int64, paths ...string) []*entity.FileSelection {
	if len(paths) == 0 {
		paths = []string{""}
	}
	selections := make([]*entity.FileSelection, 0, len(paths))
	for _, path := range paths {
		selections = append(selections, &entity.FileSelection{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: id, Path: path}}, Scope: entity.FileScope_FILE_SCOPE_ALL})
	}
	return selections
}

func TestScanSelectionAndPolicyValidation(t *testing.T) {
	location := func(path string) *entity.FileSelection {
		return &entity.FileSelection{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: 1, Path: path}}}
	}
	for _, test := range []struct {
		name string
		spec *entity.ScanJobSpec
		bad  bool
	}{
		{name: "whole Location", spec: &entity.ScanJobSpec{Selections: scanLocationSelections(1)}},
		{name: "selected root", spec: &entity.ScanJobSpec{Selections: []*entity.FileSelection{location("")}}},
		{name: "relative file", spec: &entity.ScanJobSpec{Selections: []*entity.FileSelection{location("folder/file")}}},
		{name: "escape", spec: &entity.ScanJobSpec{Selections: []*entity.FileSelection{location("../file")}}, bad: true},
		{name: "negative source", spec: &entity.ScanJobSpec{Selections: scanLocationSelections(1), MediaId: -1}, bad: true},
		{name: "mixed sources", spec: &entity.ScanJobSpec{Selections: scanLocationSelections(1), MediaId: 1}, bad: true},
		{name: "check cached", spec: &entity.ScanJobSpec{MediaId: 1, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES, SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY}, bad: true},
		{name: "check read", spec: &entity.ScanJobSpec{MediaId: 1, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES, SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ}},
		{name: "original inventory", spec: &entity.ScanJobSpec{Selections: scanLocationSelections(1), ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_INVENTORY}, bad: true},
		{name: "invalid policy", spec: &entity.ScanJobSpec{Selections: scanLocationSelections(1), SignaturePolicy: -1}, bad: true},
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

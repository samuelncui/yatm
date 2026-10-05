//go:build e2e

package e2e

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func (c *cliConnection) arguments(method string, request any) ([]string, error) {
	// Translate explicit request shapes; unsupported business operations must add CLI coverage.
	switch r := request.(type) {
	case *entity.GetJobRequest:
		return []string{"job", "get", decimal(r.Id)}, nil
	case *entity.GetJobLogRequest:
		args := []string{"job", "log", decimal(r.Id)}
		if r.Offset != nil {
			args = append(args, "--offset", decimal(*r.Offset))
		}
		return args, nil
	case *entity.CancelJobRequest:
		return []string{"job", "cancel", decimal(r.Id)}, nil
	case *entity.DeleteJobsRequest:
		args := []string{"job", "delete"}
		for _, id := range r.Ids {
			args = append(args, decimal(id))
		}
		return args, nil
	case *entity.ListJobsRequest:
		args := []string{"job", "list"}
		if r.Filter != nil {
			if r.Filter.Limit != nil {
				args = append(args, "--limit", decimal(*r.Filter.Limit))
			}
			if r.Filter.BeforeId != nil {
				args = append(args, "--before-id", decimal(*r.Filter.BeforeId))
			}
			if r.Filter.Offset != nil {
				args = append(args, "--offset", decimal(*r.Filter.Offset))
			}
			if r.Filter.SnapshotRevision != nil {
				args = append(args, "--snapshot-revision", decimal(*r.Filter.SnapshotRevision))
			}
		}
		return args, nil
	case *entity.GetFileRequest:
		return filesGetArguments(r.Reference)
	case *entity.ListFilesRequest:
		return filesListArguments(r.Directory, r.Scope, r.Include, "", "", 0, false)
	case *entity.SearchFilesRequest:
		return filesListArguments(r.Directory, r.Scope, r.Include, r.Query, r.Cursor, r.Limit, r.Recursive)
	case *entity.UpdateFilesMetadataRequest:
		args := []string{"files", "metadata"}
		for _, ref := range r.References {
			selection, err := selectionArgument(ref)
			if err != nil {
				return nil, err
			}
			args = append(args, selection...)
		}
		for _, tag := range r.AddTags {
			args = append(args, "--add-tag", tag)
		}
		for _, tag := range r.RemoveTags {
			args = append(args, "--remove-tag", tag)
		}
		if r.Note != nil {
			args = append(args, "--note", *r.Note)
		}
		return args, nil
	case *entity.ListTagsRequest:
		args := []string{"tag", "list"}
		if r.Prefix != nil {
			args = append(args, "--prefix", *r.Prefix)
		}
		if r.Limit != nil {
			args = append(args, "--limit", decimal(*r.Limit))
		}
		if r.Cursor != nil {
			args = append(args, "--cursor", *r.Cursor)
		}
		return args, nil
	case *entity.ListDevicesRequest:
		return []string{"tape", "device", "list"}, nil
	case *entity.ListMediaRequest:
		if value := r.GetIds(); value != nil {
			args := []string{"media", "get"}
			for _, id := range value.Ids {
				args = append(args, decimal(id))
			}
			return args, nil
		}
		args := []string{"media", "list"}
		if value := r.GetList(); value != nil {
			for _, kind := range value.Kinds {
				args = append(args, "--kind", strings.ToLower(strings.TrimPrefix(kind.String(), "MEDIA_KIND_")))
			}
			if value.Limit != nil {
				args = append(args, "--limit", decimal(*value.Limit))
			}
			if value.Offset != nil {
				args = append(args, "--offset", decimal(*value.Offset))
			}
		}
		return args, nil
	case *entity.ListMediaPositionsRequest:
		args := []string{"media", "positions", decimal(r.Id), "--directory", r.Directory}
		if r.Limit != nil {
			args = append(args, "--limit", decimal(*r.Limit))
		}
		if r.AfterPath != nil {
			args = append(args, "--after-path", *r.AfterPath)
		}
		return args, nil
	case *entity.DeleteMediaRequest:
		args := []string{"media", "delete"}
		for _, id := range r.Ids {
			args = append(args, decimal(id))
		}
		return args, nil
	case *entity.InspectMediaRequest:
		if value := r.GetTape(); value != nil {
			args := []string{"media", "inspect", "tape", "--device", value.Device}
			if r.Identity != nil {
				args = append(args, "--identity", *r.Identity)
			}
			return args, nil
		}
		return []string{"media", "inspect", "volume", "--uuid", r.GetVolume().Uuid}, nil
	case *entity.InitializeVolumeRequest:
		typeName := "hdd"
		if r.Profile.Type == entity.VolumeType_VOLUME_TYPE_HM_SMR {
			typeName = "hm-smr"
		}
		return []string{"volume", "initialize", r.MountPoint, "--name", r.Name, "--type", typeName, "--serial-number", r.Profile.SerialNumber}, nil
	case *entity.RegisterVolumeRequest:
		return []string{"volume", "register", r.MountPoint, "--name", r.Name}, nil
	case *entity.CreateLocationRequest:
		return c.locationArguments([]string{"location", "create"}, r.Location)
	case *entity.UpdateLocationRequest:
		return c.locationArguments([]string{"location", "update", decimal(r.Location.Id)}, r.Location)
	case *entity.ListLocationsRequest:
		return []string{"location", "list", "--after-id", decimal(r.AfterId), "--limit", pageSize(r.Limit)}, nil
	case *entity.GetLocationRequest:
		return []string{"location", "get", decimal(r.Id)}, nil
	case *entity.DeleteLocationRequest:
		if method == entity.LocationService_Delete_FullMethodName {
			return []string{"location", "delete", decimal(r.Id)}, nil
		}
	case *entity.CreateScanJobRequest:
		return scanArguments(r)
	case *entity.ListScanJobEntriesRequest:
		args := []string{"scan", "results", decimal(r.Id), "--limit", pageSize(r.Limit)}
		if r.Cursor != "" {
			args = append(args, "--cursor", r.Cursor)
		}
		if r.Offset != nil {
			args = append(args, "--offset", decimal(*r.Offset))
		}
		if r.Order == entity.JobResultOrder_JOB_RESULT_ORDER_DESCENDING {
			args = append(args, "--order", "desc")
		}
		if r.IncludeTotal {
			args = append(args, "--include-total")
		}
		return args, nil
	case *entity.GetScanJobProgressRequest:
		return []string{"job", "progress", decimal(r.Id)}, nil
	case *entity.GetArchiveJobProgressRequest:
		return []string{"job", "progress", decimal(r.Id)}, nil
	case *entity.GetRestoreJobProgressRequest:
		return []string{"job", "progress", decimal(r.Id)}, nil
	case *entity.GetFileVersionRequest:
		return []string{"files", "version", decimal(r.Id)}, nil
	case *entity.ListFileVersionsRequest:
		return []string{"files", "versions", decimal(r.FileId), "--after-id", decimal(r.AfterId), "--limit", pageSize(r.Limit)}, nil
	case *entity.ListContentCopiesRequest:
		return []string{"files", "copies", "--signature", hex.EncodeToString(r.Signature), "--after-id", decimal(r.AfterId), "--limit", pageSize(r.Limit)}, nil
	case *entity.CreateArchiveJobRequest:
		selections, err := selectionArguments(r.Spec.Selections)
		if err != nil {
			return nil, err
		}
		args := append([]string{"archive", "create", "--priority", decimal(r.Priority)}, selections...)
		if r.PreviewPolicy != entity.PreviewPolicy_PREVIEW_POLICY_UNSPECIFIED {
			args = append(args, "--preview-policy", previewPolicyArgument(r.PreviewPolicy))
		}
		if r.ForceRehash {
			args = append(args, "--force-rehash")
		}
		return args, nil
	case *entity.WriteArchiveMediaRequest:
		if target := r.Target.GetVolume(); target != nil {
			return []string{"archive", "write", "volume", decimal(r.Id), "--uuid", target.Uuid}, nil
		}
		target := r.Target.GetTape()
		args := []string{"archive", "write", "tape", "append", decimal(r.Id), "--device", target.Device, "--barcode", target.Barcode}
		if target.Mode == entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT {
			args[3] = "format"
			args = append(args, "--name", target.Name, "--confirm-format", target.Barcode)
		}
		return args, nil
	case *entity.ListArchiveJobFilesRequest:
		return jobResultPageArguments([]string{"archive", "files", decimal(r.Id)}, r.Limit, r.Cursor, r.Order, r.Offset, r.IncludeTotal, r.FilterStatus), nil
	case *entity.CreateRestoreJobRequest:
		if r.Spec.Destination == nil {
			return nil, fmt.Errorf("E2E Restore requires a registered destination")
		}
		selections, err := selectionArguments(r.Spec.Selections)
		if err != nil {
			return nil, err
		}
		args := append([]string{"restore", "create", "--priority", decimal(r.Priority), "--target-location", decimal(r.Spec.Destination.LocationId), "--directory", r.Spec.Destination.Path}, selections...)
		if r.Spec.AllowDamagedCopies {
			args = append(args, "--allow-damaged-copies")
		}
		for _, id := range r.Spec.FileVersionIds {
			args = append(args, decimal(id))
		}
		return args, nil
	case *entity.RestoreMediaRequest:
		if target := r.Target.GetVolume(); target != nil {
			return []string{"restore", "run", "volume", decimal(r.Id), "--uuid", target.Uuid}, nil
		}
		return []string{"restore", "run", "tape", decimal(r.Id), "--device", r.Target.GetTape().Device}, nil
	case *entity.ListRestoreJobMediaRequest:
		return jobResultPageArguments([]string{"restore", "media", decimal(r.Id)}, r.Limit, r.Cursor, r.Order, r.Offset, r.IncludeTotal, r.FilterStatus), nil
	case *entity.ListRestoreJobFilesRequest:
		args := []string{"restore", "files", decimal(r.Id)}
		if r.MediaId != nil {
			args = append(args, "--media-id", decimal(*r.MediaId))
		}
		return jobResultPageArguments(args, r.Limit, r.Cursor, r.Order, r.Offset, r.IncludeTotal, r.FilterStatus), nil
	}
	return nil, fmt.Errorf("no CLI acceptance mapping for %s (%T)", method, request)
}

func scanArguments(request *entity.CreateScanJobRequest) ([]string, error) {
	// Preserve explicit policies; leave unspecified values to the CLI defaults.
	policies := map[entity.ScanSignaturePolicy]string{entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY: "known-only", entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FILL_MISSING: "fill-missing", entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ: "force-read"}
	results := map[entity.ScanResultPolicy]string{entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY: "report", entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS: "originals", entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_INVENTORY: "inventory", entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES: "verify"}
	spec := request.Spec
	selections, err := selectionArguments(spec.Selections)
	if err != nil {
		return nil, err
	}
	args := append([]string{"scan", "create", "--priority", decimal(request.Priority)}, selections...)
	if spec.SignaturePolicy != entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_UNSPECIFIED {
		args = append(args, "--signature", policies[spec.SignaturePolicy])
	}
	if spec.ResultPolicy != entity.ScanResultPolicy_SCAN_RESULT_POLICY_UNSPECIFIED {
		args = append(args, "--result", results[spec.ResultPolicy])
	}
	if spec.MediaId != 0 {
		args = append(args, "--media-id", decimal(spec.MediaId))
	}
	if spec.CompareLibrary {
		args = append(args, "--compare-library")
	}
	if spec.PreviewPolicy != entity.PreviewPolicy_PREVIEW_POLICY_UNSPECIFIED {
		args = append(args, "--preview-policy", previewPolicyArgument(spec.PreviewPolicy))
	}
	return args, nil
}

func previewPolicyArgument(policy entity.PreviewPolicy) string {
	switch policy {
	case entity.PreviewPolicy_PREVIEW_POLICY_NONE:
		return "none"
	case entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY:
		return "missing-only"
	case entity.PreviewPolicy_PREVIEW_POLICY_REGENERATE_ALL:
		return "regenerate-all"
	default:
		return policy.String()
	}
}

func (c *cliConnection) locationArguments(args []string, location *entity.Location) ([]string, error) {
	// Preserve the chooser preference and raw Ignore text without inventing permissions.
	args = append(args, "--name", location.Name, "--root", location.RootPath)
	if location.RestoreTarget {
		args = append(args, "--restore-target")
	}
	if location.Config.GetUseMmap() {
		args = append(args, "--use-mmap")
	}

	// Pass raw Ignore text through the CLI's file input.
	if location.Config.GetIgnore() == nil {
		return args, nil
	}
	filename := filepath.Join(c.directory, "ignore.txt")
	if err := os.WriteFile(filename, []byte(location.Config.GetIgnore().GetText()), 0o600); err != nil {
		return nil, err
	}
	return append(args, "--ignore-file", filename), nil
}

func pageSize(limit int32) string {
	if limit == 0 {
		return "100"
	}
	return decimal(int64(limit))
}

func scopeArgument(scope entity.FileScope) string {
	switch scope {
	case entity.FileScope_FILE_SCOPE_SAVED:
		return "saved"
	case entity.FileScope_FILE_SCOPE_DEFAULT:
		return "default"
	default:
		return "all"
	}
}

func jobResultPageArguments(args []string, limit int32, cursor string, order entity.JobResultOrder, offset *int64, includeTotal bool, statuses []entity.CopyStatus) []string {
	args = append(args, "--limit", pageSize(limit))
	if cursor != "" {
		args = append(args, "--cursor", cursor)
	}
	if order == entity.JobResultOrder_JOB_RESULT_ORDER_DESCENDING {
		args = append(args, "--order", "desc")
	}
	if offset != nil {
		args = append(args, "--offset", decimal(*offset))
	}
	if includeTotal {
		args = append(args, "--include-total")
	}
	for _, status := range statuses {
		args = append(args, "--status", strings.ToLower(strings.TrimPrefix(status.String(), "COPY_STATUS_")))
	}
	return args
}

func filesGetArguments(ref *entity.FileOperationRef) ([]string, error) {
	args, err := filesReferenceArguments([]string{"files", "get"}, ref)
	if err != nil {
		return nil, err
	}
	return args, nil
}

// filesListArguments translates one Files read into the `ls` invocation that expresses it: a
// listing names only its directory, while a query adds the page flags it keeps.
func filesListArguments(ref *entity.FileOperationRef, scope entity.FileScope, include []entity.FilesInclude, query, cursor string, limit int32, recursive bool) ([]string, error) {
	args, err := filesReferenceArguments([]string{"ls"}, ref)
	if err != nil {
		return nil, err
	}
	if query != "" {
		args = append(args, "--query", query)
	}
	if recursive {
		args = append(args, "--recursive")
	}
	if cursor != "" {
		args = append(args, "--cursor", cursor)
	}
	if limit > 0 {
		args = append(args, "--limit", decimal(int64(limit)))
	}
	args = append(args, "--scope", scopeArgument(scope))
	for _, group := range include {
		switch group {
		case entity.FilesInclude_FILES_INCLUDE_ATTRIBUTES:
			args = append(args, "--long")
		case entity.FilesInclude_FILES_INCLUDE_STATUS:
			args = append(args, "--status")
		case entity.FilesInclude_FILES_INCLUDE_OPERATIONS:
			args = append(args, "--include", "operations")
		case entity.FilesInclude_FILES_INCLUDE_NAVIGATION:
			args = append(args, "--include", "navigation")
		default:
			return nil, fmt.Errorf("unsupported Files include: %v", group)
		}
	}
	return args, nil
}

func filesReferenceArguments(args []string, ref *entity.FileOperationRef) ([]string, error) {
	if ref == nil {
		return nil, fmt.Errorf("Files reference is required")
	}
	if id, ok := ref.Target.(*entity.FileOperationRef_FileId); ok {
		return append(args, "--file-id", decimal(id.FileId)), nil
	}
	if location, ok := ref.Target.(*entity.FileOperationRef_Location); ok {
		return append(args, "--location-id", decimal(location.Location.LocationId), "--path", location.Location.Path), nil
	}
	return nil, fmt.Errorf("unsupported Files reference %T", ref.Target)
}

func selectionArgument(ref *entity.FileOperationRef) ([]string, error) {
	if ref == nil {
		return nil, fmt.Errorf("Files metadata reference is required")
	}
	if id, ok := ref.Target.(*entity.FileOperationRef_FileId); ok {
		return []string{"--file-id", decimal(id.FileId)}, nil
	}
	if location, ok := ref.Target.(*entity.FileOperationRef_Location); ok {
		return []string{"--location", decimal(location.Location.LocationId) + ":" + location.Location.Path}, nil
	}
	return nil, fmt.Errorf("unsupported Files metadata reference %T", ref.Target)
}

func TestCLIEnumArguments(t *testing.T) {
	// Archive defaults to no Preview without emitting the renamed zero enum as CLI text.
	selection := &entity.FileSelection{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: 7}}, Scope: entity.FileScope_FILE_SCOPE_ALL}
	archive := &entity.CreateArchiveJobRequest{Spec: &entity.ArchiveJobSpec{Selections: []*entity.FileSelection{selection}}}
	args, err := new(cliConnection).arguments("", archive)
	require.NoError(t, err)
	require.NotContains(t, args, "--preview-policy")

	// An explicit generation policy keeps its user-facing spelling.
	archive.PreviewPolicy = entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY
	args, err = new(cliConnection).arguments("", archive)
	require.NoError(t, err)
	require.Equal(t, []string{"--preview-policy", "missing-only"}, args[len(args)-2:])

	// Scan uses CLI defaults only for unspecified policies.
	scan := &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{Selections: []*entity.FileSelection{selection}, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS}}
	args, err = scanArguments(scan)
	require.NoError(t, err)
	require.NotContains(t, args, "--signature")
	require.NotContains(t, args, "--preview-policy")
	require.Contains(t, args, "originals")

	// Copy status filters also omit the enum's protobuf prefix.
	args = jobResultPageArguments(nil, 0, "", entity.JobResultOrder_JOB_RESULT_ORDER_UNSPECIFIED, nil, false, []entity.CopyStatus{entity.CopyStatus_COPY_STATUS_PENDING})
	require.Equal(t, []string{"--status", "pending"}, args[len(args)-2:])
}

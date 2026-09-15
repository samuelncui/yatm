//go:build e2e

package e2e

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/entity"
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
	case *entity.RetryJobIndexRequest:
		return []string{"job", "retry-index", decimal(r.Id)}, nil
	case *entity.DeleteJobsRequest:
		args := []string{"job", "delete", "--confirm"}
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
	case *entity.FileGetRequest:
		args := []string{"file", "get", decimal(r.Id), "--scope", scopeArgument(r.Scope)}
		if r.GetNeedSize() {
			args = append(args, "--need-size")
		}
		if r.Limit > 0 {
			args = append(args, "--limit", decimal(int64(r.Limit)))
		}
		if r.Cursor != "" {
			args = append(args, "--cursor", r.Cursor)
		}
		return args, nil
	case *entity.FileListParentsRequest:
		return []string{"file", "parents", decimal(r.Id)}, nil
	case *entity.FileMetadataEditRequest:
		args := []string{"file", "metadata"}
		for _, id := range r.Ids {
			args = append(args, decimal(id))
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
	case *entity.FileSearchRequest:
		args := []string{"file", "search", r.Query, "--scope", scopeArgument(r.Scope)}
		if r.Limit != nil {
			args = append(args, "--limit", decimal(*r.Limit))
		}
		if r.Cursor != nil {
			args = append(args, "--cursor", *r.Cursor)
		}
		return args, nil
	case *entity.TagListRequest:
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
	case *entity.DeviceListRequest:
		return []string{"tape", "device", "list"}, nil
	case *entity.MediaListRequest:
		if value := r.GetMget(); value != nil {
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
	case *entity.MediaGetPositionsRequest:
		args := []string{"media", "positions", decimal(r.Id), "--directory", r.Directory}
		if r.Limit != nil {
			args = append(args, "--limit", decimal(*r.Limit))
		}
		if r.AfterPath != nil {
			args = append(args, "--after-path", *r.AfterPath)
		}
		return args, nil
	case *entity.MediaDeleteRequest:
		args := []string{"media", "delete", "--confirm"}
		for _, id := range r.Ids {
			args = append(args, decimal(id))
		}
		return args, nil
	case *entity.MediaInspectRequest:
		if value := r.GetTape(); value != nil {
			args := []string{"media", "inspect", "tape", "--device", value.Device}
			if r.Identity != nil {
				args = append(args, "--identity", *r.Identity)
			}
			return args, nil
		}
		return []string{"media", "inspect", "volume", "--uuid", r.GetVolume().Uuid}, nil
	case *entity.VolumeInitializeRequest:
		typeName := "hdd"
		if r.Profile.Type == entity.VolumeType_VOLUME_TYPE_HM_SMR {
			typeName = "hm-smr"
		}
		return []string{"volume", "initialize", r.MountPoint, "--name", r.Name, "--type", typeName, "--serial-number", r.Profile.SerialNumber}, nil
	case *entity.VolumeRegisterRequest:
		return []string{"volume", "register", r.MountPoint, "--name", r.Name}, nil
	case *entity.CreateLocationRequest:
		return c.locationArguments([]string{"location", "create"}, r.Location)
	case *entity.UpdateLocationRequest:
		return c.locationArguments([]string{"location", "update", decimal(r.Location.Id), "--revision", decimal(r.Location.Revision)}, r.Location)
	case *entity.ListLocationsRequest:
		return []string{"location", "list", "--after-id", decimal(r.AfterId), "--limit", pageSize(r.Limit)}, nil
	case *entity.LocationRef:
		switch method {
		case entity.LocationService_Get_FullMethodName:
			return []string{"location", "get", decimal(r.Id)}, nil
		case entity.LocationService_Confirm_FullMethodName:
			return []string{"location", "confirm", decimal(r.Id), "--revision", decimal(r.Revision)}, nil
		case entity.LocationService_Delete_FullMethodName:
			return []string{"location", "delete", decimal(r.Id), "--revision", decimal(r.Revision), "--confirm"}, nil
		}
	case *entity.ListLocationEntriesRequest:
		return []string{"location", "entries", decimal(r.LocationId), "--parent", r.ParentPath, "--cursor", r.Cursor, "--name", r.NameFilter, "--limit", pageSize(r.Limit)}, nil
	case *entity.CreateScanJobRequest:
		return scanArguments(r)
	case *entity.ListScanJobEntriesRequest:
		args := []string{"scan", "results", decimal(r.Id), "--limit", pageSize(r.Limit)}
		if r.AfterId != nil {
			args = append(args, "--after-id", decimal(*r.AfterId))
		}
		return args, nil
	case *entity.GetScanJobProgressRequest:
		return []string{"job", "progress", decimal(r.Id)}, nil
	case *entity.GetArchiveJobProgressRequest:
		return []string{"job", "progress", decimal(r.Id)}, nil
	case *entity.GetRestoreJobProgressRequest:
		return []string{"job", "progress", decimal(r.Id)}, nil
	case *entity.GetFileStateRequest:
		return []string{"file", "state", decimal(r.FileId)}, nil
	case *entity.GetFileVersionRequest:
		return []string{"file", "version", decimal(r.Id)}, nil
	case *entity.ListFileVersionsRequest:
		return []string{"file", "versions", decimal(r.FileId), "--after-id", decimal(r.AfterId), "--limit", pageSize(r.Limit)}, nil
	case *entity.ListContentCopiesRequest:
		return []string{"file", "copies", "--signature", hex.EncodeToString(r.Signature), "--after-id", decimal(r.AfterId), "--limit", pageSize(r.Limit)}, nil
	case *entity.CreateArchiveJobRequest:
		if len(r.Spec.Sources) > 0 {
			return nil, fmt.Errorf("E2E must register and scan Locations before selecting archive content")
		}
		selections, err := selectionArguments(r.Spec.Selections, r.Spec.FileIds)
		if err != nil {
			return nil, err
		}
		args := append([]string{"archive", "create", "--priority", decimal(r.Priority)}, selections...)
		args = append(args, "--preview-policy", previewPolicyArgument(r.PreviewPolicy))
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
		return copyPageArguments([]string{"archive", "files", decimal(r.Id)}, r.Limit, r.Offset, r.FilterStatus), nil
	case *entity.CreateRestoreJobRequest:
		if r.Spec.Destination == nil {
			return nil, fmt.Errorf("E2E Restore requires a registered destination")
		}
		selections, err := selectionArguments(r.Spec.Selections, nil)
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
		return copyPageArguments([]string{"restore", "media", decimal(r.Id)}, r.Limit, r.Offset, r.FilterStatus), nil
	case *entity.ListRestoreJobFilesRequest:
		return copyPageArguments([]string{"restore", "files", decimal(r.Id), "--media-id", decimal(r.MediaId)}, r.Limit, r.Offset, r.FilterStatus), nil
	}
	return nil, fmt.Errorf("no CLI acceptance mapping for %s (%T)", method, request)
}

func scanArguments(request *entity.CreateScanJobRequest) ([]string, error) {
	// Encode explicit policies rather than deriving a different CLI workflow.
	policies := map[entity.ScanSignaturePolicy]string{entity.ScanSignaturePolicy_KNOWN_ONLY: "known-only", entity.ScanSignaturePolicy_FILL_MISSING: "fill-missing", entity.ScanSignaturePolicy_FORCE_READ: "force-read"}
	results := map[entity.ScanResultPolicy]string{entity.ScanResultPolicy_REPORT_ONLY: "report", entity.ScanResultPolicy_PUBLISH_ORIGINALS: "originals", entity.ScanResultPolicy_PUBLISH_INVENTORY: "inventory", entity.ScanResultPolicy_VERIFY_COPIES: "verify"}
	spec := request.Spec
	selections, err := selectionArguments(spec.Selections, nil)
	if err != nil {
		return nil, err
	}
	args := append([]string{"scan", "create", "--priority", decimal(request.Priority), "--signature", policies[spec.SignaturePolicy], "--result", results[spec.ResultPolicy]}, selections...)
	if spec.MediaId != 0 {
		args = append(args, "--media-id", decimal(spec.MediaId))
	}
	if spec.LocationId != 0 {
		args = append(args, "--location-id", decimal(spec.LocationId))
	}
	for _, path := range spec.Paths {
		args = append(args, "--path", path)
	}
	if spec.CompareLibrary {
		args = append(args, "--compare-library")
	}
	args = append(args, "--preview-policy", previewPolicyArgument(spec.PreviewPolicy))
	return args, nil
}

func previewPolicyArgument(policy entity.PreviewPolicy) string {
	switch policy {
	case entity.PreviewPolicy_PREVIEW_NONE:
		return "none"
	case entity.PreviewPolicy_PREVIEW_MISSING_ONLY:
		return "missing-only"
	case entity.PreviewPolicy_PREVIEW_REGENERATE_ALL:
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
	if location.WriteTrackingUuid {
		args = append(args, "--write-tracking-uuid")
	}
	if location.Ignore == nil {
		return args, nil
	}
	filename := filepath.Join(c.directory, "ignore.txt")
	if err := os.WriteFile(filename, []byte(location.Ignore.Text), 0o600); err != nil {
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

func copyPageArguments(args []string, limit int32, offset *int64, statuses []entity.CopyStatus) []string {
	args = append(args, "--limit", pageSize(limit))
	if offset != nil {
		args = append(args, "--offset", decimal(*offset))
	}
	for _, status := range statuses {
		args = append(args, "--status", strings.ToLower(status.String()))
	}
	return args
}

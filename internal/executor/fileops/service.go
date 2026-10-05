package fileops

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/treeops"
	"google.golang.org/protobuf/proto"
)

type service struct {
	exe *executor.Executor
	// declaredLocations restricts a scoped service to the Locations it announced.
	declaredLocations map[int64]bool
}

// ResultStream exposes request-bound results without coupling the shared engine to an RPC method.
type ResultStream interface {
	Context() context.Context
	Send(*entity.FileOperationResult) error
}

// Mkdir creates directories through the shared organization engine.
func Mkdir(exe *executor.Executor, req *entity.MkdirFilesRequest, stream ResultStream) error {
	return (&service{exe: exe}).execute(&entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR,
		Destination: req.GetDestination(), Name: req.GetName()}, req.GetDryrun(), stream)
}

// Move moves or renames selected entries through the shared organization engine.
func Move(exe *executor.Executor, req *entity.MoveFilesRequest, stream ResultStream) error {
	return (&service{exe: exe}).execute(&entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_MOVE,
		Sources: req.GetSources(), Destination: req.GetDestination(), Name: req.GetName()}, req.GetDryrun(), stream)
}

// Remove moves selected entries to their source's recycle bin.
func Remove(exe *executor.Executor, req *entity.RemoveFilesRequest, stream ResultStream) error {
	return (&service{exe: exe}).execute(&entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE,
		Sources: req.GetSources()}, req.GetDryrun(), stream)
}

func (s *service) execute(input *entity.FileOperationSpec, dryRun bool, stream ResultStream) (returnErr error) {
	// Both scopes share intent validation and request-bound results, not separate public workflows.
	spec, logical, err := validateSpec(input)
	if err != nil {
		return err
	}
	return s.executeTree(spec, logical, dryRun, stream)
}

func (s *service) executeTree(spec *entity.FileOperationSpec, logical, dryRun bool, stream ResultStream) (returnErr error) {
	// Only construction differs; both namespaces run the same planner and executor.
	ctx := stream.Context()
	var store treeops.Store
	var transaction treeops.Transaction
	var config *config
	var err error
	options := treeops.Options{NativeMove: true}
	if logical {
		store, transaction = s.exe.Lib().FileTree(), s.exe.Lib().FileTreeTransaction
	} else {
		config, err = s.validateLocation(ctx, spec)
	}
	if err != nil {
		return err
	}
	op, err := newOperation(ctx, s.exe)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, op.Close()) }()
	if !logical {
		location, err := op.currentLocation(ctx, config)
		if err != nil {
			return err
		}
		store = &locationTree{op: op, location: location, config: config}
	}
	engine, err := treeops.New(ctx, op.DB, store, options, transaction)
	if err != nil {
		return err
	}
	ref := func(value *entity.FileOperationRef) string {
		if logical {
			return strconv.FormatInt(value.GetFileId(), 10)
		}
		return "path:" + value.GetLocation().GetPath()
	}
	request := treeops.Request{Name: spec.Name}
	switch spec.Kind {
	case entity.FileOperationKind_FILE_OPERATION_KIND_MOVE:
		request.Kind = treeops.Move
	case entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR:
		request.Kind = treeops.Mkdir
	case entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE:
		request.Kind = treeops.Delete
	}
	for _, source := range spec.Sources {
		request.Sources = append(request.Sources, ref(source))
	}
	if spec.Destination != nil {
		request.Destination = ref(spec.Destination)
	}
	if err := engine.Prepare(ctx, request); err != nil {
		return err
	}
	count, size, err := engine.Totals(ctx)
	if err != nil {
		return err
	}
	summary := &entity.FileOperationSummary{TotalItemCount: count, UnprocessedCount: count, TotalBytes: size, Dryrun: dryRun}
	if err := stream.Send(&entity.FileOperationResult{Summary: summary}); err != nil {
		return err
	}

	// A dry run reports the resolved plan per planned entry and changes nothing.
	if dryRun {
		return reportPlan(ctx, engine, summary, stream)
	}

	// Every primitive exposes its actual paths, including untouched entries after a partial failure.
	if err := engine.Run(ctx, func(root treeops.Result) error {
		entry := &entity.FileOperationEntry{Id: root.ID, SourcePath: root.Source.Path, TargetPath: root.Target.Path,
			Directory: root.Source.Directory || root.Target.Directory, SizeBytes: root.Source.Size, Error: root.Error,
			Outcome: entity.FileOperationOutcome_FILE_OPERATION_OUTCOME_SUCCEEDED}
		if root.FileID != 0 {
			entry.FileId = &root.FileID
		}
		switch root.Outcome {
		case treeops.PublicationPending:
			entry.Outcome = entity.FileOperationOutcome_FILE_OPERATION_OUTCOME_PUBLICATION_PENDING
			summary.PublicationPendingCount++
		case treeops.Failed:
			entry.Outcome = entity.FileOperationOutcome_FILE_OPERATION_OUTCOME_FAILED
			summary.FailedCount++
		case treeops.Succeeded:
			summary.SucceededCount++
		default:
			entry.Outcome = entity.FileOperationOutcome_FILE_OPERATION_OUTCOME_UNPROCESSED
		}
		if entry.Outcome != entity.FileOperationOutcome_FILE_OPERATION_OUTCOME_UNPROCESSED {
			summary.UnprocessedCount--
		}
		return stream.Send(&entity.FileOperationResult{Entry: entry, Summary: summary})
	}); err != nil {
		return err
	}
	summary.Completed = true
	return stream.Send(&entity.FileOperationResult{Summary: summary})
}

// reportPlan streams the planned roots as a report, never as completed work.
func reportPlan(ctx context.Context, engine *treeops.Engine, summary *entity.FileOperationSummary, stream ResultStream) error {
	roots, err := engine.Roots(ctx)
	if err != nil {
		return err
	}
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := &entity.FileOperationEntry{Id: root.ID, SourcePath: root.Source.Path, TargetPath: root.Target.Path,
			Directory: root.Source.Directory || root.Target.Directory, SizeBytes: root.Source.Size, Error: root.Error,
			Outcome: entity.FileOperationOutcome_FILE_OPERATION_OUTCOME_UNPROCESSED}
		if err := stream.Send(&entity.FileOperationResult{Entry: entry, Summary: summary}); err != nil {
			return err
		}
	}
	summary.Completed = true
	return stream.Send(&entity.FileOperationResult{Summary: summary})
}

func validateSpec(input *entity.FileOperationSpec) (*entity.FileOperationSpec, bool, error) {
	// Validate explicit intent before acquiring resources or performing filesystem mutations.
	if input == nil {
		return nil, false, fmt.Errorf("file operation specification is missing")
	}
	spec := proto.Clone(input).(*entity.FileOperationSpec)
	if len(spec.Sources) > 1000 {
		return nil, false, fmt.Errorf("select at most 1000 operation roots")
	}
	switch spec.Kind {
	case entity.FileOperationKind_FILE_OPERATION_KIND_MOVE:
		if len(spec.Sources) == 0 || spec.Destination == nil {
			return nil, false, fmt.Errorf("sources and destination are required")
		}
		if spec.Name != "" && len(spec.Sources) != 1 {
			return nil, false, fmt.Errorf("a replacement name requires one source")
		}
	case entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR:
		if len(spec.Sources) != 0 || spec.Name == "" || spec.Destination == nil {
			return nil, false, fmt.Errorf("mkdir requires a destination and name, without sources")
		}
	case entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE:
		if len(spec.Sources) == 0 || spec.Destination != nil || spec.Name != "" {
			return nil, false, fmt.Errorf("delete requires sources only")
		}
	default:
		return nil, false, fmt.Errorf("unsupported file operation")
	}

	// Validate every reference before dispatch; mixed selections must never mutate either store.
	refs := append([]*entity.FileOperationRef{}, spec.Sources...)
	if spec.Destination != nil {
		refs = append(refs, spec.Destination)
	}
	logical, err := libraryReference(refs[0])
	if err != nil {
		return nil, false, err
	}
	for _, ref := range refs {
		isLibrary, err := libraryReference(ref)
		if err != nil {
			return nil, false, err
		}
		if isLibrary != logical {
			return nil, false, fmt.Errorf("file operations cannot mix Library and Location references")
		}
	}
	return spec, logical, nil
}

func libraryReference(ref *entity.FileOperationRef) (bool, error) {
	// Missing oneof values are not Library root references.
	switch target := ref.GetTarget().(type) {
	case *entity.FileOperationRef_FileId:
		if target != nil {
			return true, nil
		}
	case *entity.FileOperationRef_Location:
		if target != nil && target.Location != nil {
			return false, nil
		}
	}
	return false, fmt.Errorf("file operation reference is missing")
}

func (s *service) validateLocation(ctx context.Context, request *entity.FileOperationSpec) (*config, error) {
	// Convert the already type-checked public references once at the physical boundary.
	spec := &locationSpec{Kind: request.Kind, Name: request.Name, Destination: request.Destination.GetLocation()}
	for _, ref := range request.Sources {
		spec.Sources = append(spec.Sources, ref.GetLocation())
	}

	// One confirmed Location owns all sources and the destination for this entire request.
	ref := spec.Destination
	if ref == nil {
		ref = spec.Sources[0]
	}
	if err := s.admitLocation(ref.GetLocationId()); err != nil {
		return nil, err
	}
	location, _, info, err := s.exe.ResolveLocationEntry(ctx, ref)
	if err != nil {
		return nil, err
	}
	if spec.Destination != nil && !info.IsDir() {
		return nil, fmt.Errorf("operation destination is not a directory")
	}
	if spec.Destination != nil && executor.IsLocationTrashPath(spec.Destination.Path) {
		return nil, fmt.Errorf("Trash cannot be an operation destination")
	}
	for _, source := range spec.Sources {
		if source.GetLocationId() != location.ID {
			return nil, fmt.Errorf("operations must stay within one Location")
		}
		if source.Path == "" {
			return nil, fmt.Errorf("Location roots cannot be moved or removed")
		}
		if executor.IsLocationTrashPath(source.Path) {
			if spec.Kind != entity.FileOperationKind_FILE_OPERATION_KIND_MOVE || !executor.IsLocationTrashContent(source.Path) {
				return nil, fmt.Errorf("Trash entries can only be moved out")
			}
			if err := validateTrash(location.RootPath); err != nil {
				return nil, err
			}
		}
		if _, _, _, err := s.exe.ResolveLocationEntry(ctx, source); err != nil {
			return nil, err
		}
	}
	return &config{Spec: spec, OperationID: uuid.NewString(), LocationID: location.ID,
		RootPath: location.RootPath}, nil
}

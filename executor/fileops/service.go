package fileops

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/internal/treeops"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type service struct {
	entity.UnimplementedFileOperationServiceServer
	exe *executor.Executor
}

// RegisterService exposes request-bound file operations independently from the Job catalog.
func RegisterService(server grpc.ServiceRegistrar, exe *executor.Executor) {
	entity.RegisterFileOperationServiceServer(server, &service{exe: exe})
}

func (s *service) Execute(req *entity.ExecuteFileOperationRequest, stream entity.FileOperationService_ExecuteServer) (returnErr error) {
	// Both scopes share intent validation and request-bound results, not separate public workflows.
	spec, logical, err := validateSpec(req)
	if err != nil {
		return err
	}
	return s.executeTree(spec, logical, stream)
}

func (s *service) executeTree(spec *entity.FileOperationSpec, logical bool, stream entity.FileOperationService_ExecuteServer) (returnErr error) {
	// Only construction differs; both namespaces run the same planner and executor.
	ctx := stream.Context()
	var store treeops.Store
	var transaction treeops.Transaction
	var release func()
	var config *config
	var err error
	options := treeops.Options{NativeMove: true}
	if logical {
		release, err = s.exe.Lib().UseOnlineRead()
		store, transaction = s.exe.Lib().FileTree(), s.exe.Lib().FileTreeTransaction
	} else {
		config, release, err = s.validateLocation(ctx, spec)
	}
	if err != nil {
		return err
	}
	defer release()
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
	engine, err := treeops.New(ctx, op.db, store, options, transaction)
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
	case entity.FileOperationKind_MOVE:
		request.Kind = treeops.Move
	case entity.FileOperationKind_MAKE_DIRECTORY:
		request.Kind = treeops.Mkdir
	case entity.FileOperationKind_DELETE:
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
	summary := &entity.FileOperationSummary{TotalItems: count, Unprocessed: count, TotalBytes: size}
	if err := stream.Send(&entity.FileOperationUpdate{Summary: summary}); err != nil {
		return err
	}

	// Every primitive exposes its actual paths, including untouched entries after a partial failure.
	if err := engine.Run(ctx, func(root treeops.Result) error {
		entry := &entity.FileOperationEntry{Id: root.ID, SourcePath: root.Source.Path, TargetPath: root.Target.Path,
			IsDir: root.Source.Directory || root.Target.Directory, Size: root.Source.Size, Error: root.Error,
			Outcome: entity.FileOperationOutcome_SUCCEEDED}
		if root.FileID != 0 {
			entry.FileId = &root.FileID
		}
		switch root.Outcome {
		case treeops.PublicationPending:
			entry.Outcome = entity.FileOperationOutcome_PUBLICATION_PENDING
			summary.PublicationPending++
		case treeops.Failed:
			entry.Outcome = entity.FileOperationOutcome_FAILED
			summary.Failed++
		case treeops.Succeeded:
			summary.Succeeded++
		default:
			entry.Outcome = entity.FileOperationOutcome_UNPROCESSED
		}
		if entry.Outcome != entity.FileOperationOutcome_UNPROCESSED {
			summary.Unprocessed--
		}
		return stream.Send(&entity.FileOperationUpdate{Entry: entry, Summary: summary})
	}); err != nil {
		return err
	}
	summary.Completed = true
	return stream.Send(&entity.FileOperationUpdate{Summary: summary})
}

func validateSpec(req *entity.ExecuteFileOperationRequest) (*entity.FileOperationSpec, bool, error) {
	// Validate explicit intent before acquiring resources or performing filesystem mutations.
	if req == nil || req.Spec == nil {
		return nil, false, fmt.Errorf("file operation specification is missing")
	}
	spec := proto.Clone(req.Spec).(*entity.FileOperationSpec)
	if len(spec.Sources) > 1000 {
		return nil, false, fmt.Errorf("select at most 1000 operation roots")
	}
	switch spec.Kind {
	case entity.FileOperationKind_MOVE:
		if len(spec.Sources) == 0 || spec.Destination == nil {
			return nil, false, fmt.Errorf("sources and destination are required")
		}
		if spec.Name != "" && len(spec.Sources) != 1 {
			return nil, false, fmt.Errorf("a replacement name requires one source")
		}
	case entity.FileOperationKind_MAKE_DIRECTORY:
		if len(spec.Sources) != 0 || spec.Name == "" || spec.Destination == nil {
			return nil, false, fmt.Errorf("mkdir requires a destination and name, without sources")
		}
	case entity.FileOperationKind_DELETE:
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
	if !logical && spec.Kind == entity.FileOperationKind_DELETE && !req.ConfirmDelete {
		return nil, false, fmt.Errorf("permanent deletion requires explicit confirmation")
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

func (s *service) validateLocation(ctx context.Context, request *entity.FileOperationSpec) (*config, func(), error) {
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
	release, err := s.exe.Lib().UseOnlineSource(ref.GetLocationId())
	if err != nil {
		return nil, nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			release()
		}
	}()
	location, _, info, err := s.exe.ResolveLocationEntry(ctx, ref)
	if err != nil {
		return nil, nil, err
	}
	if spec.Destination != nil && !info.IsDir() {
		return nil, nil, fmt.Errorf("operation destination is not a directory")
	}
	for _, source := range spec.Sources {
		if source.GetLocationId() != location.ID || source.GetBindingToken() != location.BindingToken {
			return nil, nil, fmt.Errorf("operations must stay within one current Location binding")
		}
		if source.Path == "" {
			return nil, nil, fmt.Errorf("Location roots cannot be moved, copied or deleted")
		}
		if _, _, _, err := s.exe.ResolveLocationEntry(ctx, source); err != nil {
			return nil, nil, err
		}
	}
	accepted = true
	return &config{Spec: spec, OperationID: uuid.NewString(), LocationID: location.ID,
		RootPath: location.RootPath, BindingToken: location.BindingToken}, release, nil
}

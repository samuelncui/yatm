package apis

import (
	"context"
	"errors"
	"os"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (api *API) FileGet(ctx context.Context, req *entity.FileGetRequest) (*entity.FileGetReply, error) {
	// Only ID zero represents the virtual root; stale IDs must not become empty directory views.
	if req == nil || (req.Id < 0 && req.Id != library.TrashFileID) {
		return nil, status.Error(codes.InvalidArgument, "File ID must be nonnegative or the reserved Trash ID")
	}
	// Keep ID-based lookup and hydration within one catalog maintenance admission.
	release, err := api.lib.UseOnlineRead()
	if err != nil {
		return nil, onlineError(err)
	}
	defer release()

	var libFile *library.File
	if req.Id != 0 {
		var err error
		libFile, err = api.lib.GetFile(ctx, req.Id)
		if err != nil {
			return nil, onlineError(err)
		}
	}

	// Load the requested direct-child view before hydrating all public Tags together.
	page, err := api.lib.ListFiles(ctx, req.Id, req.Scope, req.Cursor, int64(req.Limit))
	if err != nil {
		return nil, err
	}
	children := page.Files
	files := make([]*library.File, 0, len(children)+1)
	if libFile != nil {
		files = append(files, libFile)
	}
	files = append(files, children...)
	if err := api.hydrateFileTags(ctx, files...); err != nil {
		return nil, err
	}
	if err := api.lib.HydrateFileContent(ctx, files...); err != nil {
		return nil, err
	}
	if req.GetNeedSize() {
		for _, file := range files {
			if file.Kind != entity.FileKind_FILE_KIND_DIRECTORY {
				continue
			}
			file.Size, err = api.lib.FileTreeSize(ctx, file.ID, page.Scope)
			if err != nil {
				return nil, err
			}
		}
	}

	// Build the response after metadata hydration so every returned File is complete.
	reply := &entity.FileGetReply{Children: convertFiles(children...), NextCursor: page.NextCursor, Scope: page.Scope}
	if libFile != nil {
		reply.File = convertFiles(libFile)[0]
	}

	// Resolve display content to the Preview producer's hash/size key, independently of opaque identity.
	if libFile != nil && api.exe.Previews() != nil && len(libFile.Hash) == 32 {
		signature, _ := library.NewFileSignature(libFile.Hash, libFile.Size)
		manifest, err := api.exe.Previews().Manifest(signature)
		if err == nil {
			reply.Preview = manifest
		} else if !errors.Is(err, os.ErrNotExist) {
			logrus.WithContext(ctx).WithError(err).Warnf("read preview metadata failed, file_id=%d", libFile.ID)
		}
	}
	return reply, nil
}

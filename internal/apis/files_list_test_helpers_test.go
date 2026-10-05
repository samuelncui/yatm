package apis

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/grpc/metadata"
)

// listFiles drains one complete listing through the streaming call and returns it as one
// reply, which is what a test that reads a whole directory wants.
func listFiles(t *testing.T, ctx context.Context, service *filesService, request *entity.ListFilesRequest) (*entity.ListFilesResponse, error) {
	t.Helper()
	server := &filesListRecorder{ctx: ctx}
	if err := service.List(request, server); err != nil {
		return nil, err
	}
	return server.reply, nil
}

// listBatches drains one listing and reports the batches it arrived in.
func listBatches(t *testing.T, ctx context.Context, service *filesService, request *entity.ListFilesRequest) (batchSummary, error) {
	t.Helper()
	server := &filesListRecorder{ctx: ctx}
	if err := service.List(request, server); err != nil {
		return batchSummary{}, err
	}
	return batchSummary{sizes: server.sizes, entries: server.reply.Entries, first: server.first}, nil
}

// batchSummary is how one listing arrived, batch by batch.
type batchSummary struct {
	sizes   []int
	entries []*entity.FilesEntry
	first   *entity.ListFilesResponse
}

// filesListRecorder collects one listing's batches.
type filesListRecorder struct {
	ctx   context.Context
	reply *entity.ListFilesResponse
	first *entity.ListFilesResponse
	sizes []int
	// afterSend runs after each batch is recorded, which is how a test observes what work had
	// finished when a batch left the server.
	afterSend func(batch int)
}

func (r *filesListRecorder) Send(batch *entity.ListFilesResponse) error {
	if r.reply == nil {
		r.reply = &entity.ListFilesResponse{}
		r.first = batch
	}
	r.sizes = append(r.sizes, len(batch.Entries))
	r.reply.Entries = append(r.reply.Entries, batch.Entries...)
	if batch.Directory != nil || batch.Breadcrumbs != nil || batch.TotalEntryCount != nil {
		r.reply.Directory, r.reply.Breadcrumbs, r.reply.TotalEntryCount, r.reply.Scope = batch.Directory, batch.Breadcrumbs, batch.TotalEntryCount, batch.Scope
	}
	if r.afterSend != nil {
		r.afterSend(len(r.sizes) - 1)
	}
	return nil
}

func (r *filesListRecorder) Context() context.Context     { return r.ctx }
func (r *filesListRecorder) SendHeader(metadata.MD) error { return nil }
func (r *filesListRecorder) SetHeader(metadata.MD) error  { return nil }
func (r *filesListRecorder) SetTrailer(metadata.MD)       {}
func (r *filesListRecorder) SendMsg(any) error            { return nil }
func (r *filesListRecorder) RecvMsg(any) error            { return nil }

// Package files defines the operation layer both file sources implement.
//
// The Files service binds a request to one Source and projects what it returns; it does
// not branch on whether the rows come from the Library catalog or from a live Location.
// A Source owns what its own kind means: on a Location a move changes disk entries, on
// the Library it changes logical organization and moves the stored original.
package files

import (
	"context"
	"errors"

	"github.com/samuelncui/yatm/entity"
)

// Kind is the source family a request addresses.
type Kind int

const (
	// KindLibrary addresses logical Library directories by File ID.
	KindLibrary Kind = iota
	// KindLocation addresses a registered Location's live directory by relative path.
	KindLocation
)

// DefaultBatchSize is how many rows one listing reply carries when a caller states none.
const DefaultBatchSize = 500

// Directory addresses one directory in a Source. A Library kind uses ID and ignores Path;
// a Location kind uses both.
type Directory struct {
	Kind Kind
	ID   int64
	Path string
}

// Entry identifies one row for detail and operation calls.
type Entry struct {
	Kind Kind
	ID   int64
	Path string
}

// ErrStopListing lets a consumer end a listing early, which is how a caller that only
// needs to know the directory resolves stops after the first batch.
var ErrStopListing = errors.New("listing stopped by its consumer")

// ListRequest asks for one complete directory listing. It carries a directory and the fact
// groups the caller needs, never a cursor or a page size: a listing has no remainder.
// Query and Recursive are empty for a directory read, where they are always meaningless.
type ListRequest struct {
	Directory Directory
	Scope     entity.FileScope
	Query     string
	Recursive bool
	Include   []entity.FilesInclude
	BatchSize int
}

// ListReply is one batch of one listing. The first batch states the directory and the
// settled entry count; later batches carry rows only.
type ListReply struct {
	Entries     []*entity.FilesEntry
	Scope       entity.FileScope
	Directory   *entity.FilesEntry
	Breadcrumbs []*entity.FilesEntry
	Total       *int64
}

// SearchRequest is one bounded page of a query over the Library or one Location directory.
type SearchRequest struct {
	Directory Directory
	Scope     entity.FileScope
	Query     string
	Cursor    string
	Limit     int
	Recursive bool
	Include   []entity.FilesInclude
}

// SearchReply is one page. Total is nil when the query cannot state how many matches exist.
type SearchReply struct {
	Entries     []*entity.FilesEntry
	NextCursor  string
	Scope       entity.FileScope
	Directory   *entity.FilesEntry
	Breadcrumbs []*entity.FilesEntry
	Total       *int64
}

// ListRequest is the listing shape of the same directory, scope, query and fact groups.
func (r SearchRequest) ListRequest() ListRequest {
	return ListRequest{Directory: r.Directory, Scope: r.Scope, Query: r.Query, Recursive: r.Recursive,
		Include: r.Include, BatchSize: r.Limit}
}

// Detail is one entry's detail as its source resolves it. The caller adds what is shared
// across sources, such as logical organization when the row is an admitted Library File.
type Detail struct {
	Entry            *entity.FilesEntry
	Original         *entity.FilesOriginal
	ContentReference *entity.FileOperationRef
	// ContentSignature is the recorded identity of the current original, present only while its
	// live observation still agrees with the record.
	ContentSignature []byte
	FileID           int64
}

// Source lists, searches and reads files from one place, reporting the same shapes for its
// own kind. Emit returns the consumer's error, which stops the listing immediately.
type Source interface {
	List(ctx context.Context, req ListRequest, emit func(ListReply) error) error
	Search(ctx context.Context, req SearchRequest) (*SearchReply, error)
	Get(ctx context.Context, entry Entry) (*Detail, error)
}

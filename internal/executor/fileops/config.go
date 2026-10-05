package fileops

import "github.com/samuelncui/yatm/entity"

const batchSize = 128

type config struct {
	Spec        *locationSpec
	OperationID string
	LocationID  int64
	RootPath    string
}

// locationSpec contains only references admitted to one physical execution boundary.
type locationSpec struct {
	Kind        entity.FileOperationKind
	Sources     []*entity.LocationEntryRef
	Destination *entity.LocationEntryRef
	Name        string
}

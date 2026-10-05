package fileops

// Item retains the physical/publication failure boundary of one entry.
type Item struct {
	ID           int64
	SourcePath   string
	TargetPath   string
	Directory    bool
	PhysicalDone bool
	UnlinkEmpty  bool
}

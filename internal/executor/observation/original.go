package observation

const BatchSize = 256

// Original carries a page of Library matching facts and is never persisted.
type Original struct {
	FileID    int64
	Path      string
	Signature []byte
	Hash      []byte
	Size      int64
	Evidence  Evidence
}

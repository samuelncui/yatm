package executor

type progress struct {
	speed int64

	totalBytes, totalFiles int64
	bytes, files           int64
}

package archive

import "time"

// copySettings is one attempt's resolved Job pipeline configuration. Every limit maps onto an ACP
// run option: the manifest page size, the read buffer, the result queue, the result batch and the
// result flush interval. The runner's result writer only persists the batches ACP hands over.
type copySettings struct {
	page                int
	readBuffer          int
	resultBuffer        int
	resultBatch         int
	resultFlushInterval time.Duration
}

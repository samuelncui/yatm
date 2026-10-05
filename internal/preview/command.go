package preview

import "bytes"

const commandOutputLimit = 64 << 10

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if remaining > len(data) {
			remaining = len(data)
		}
		_, _ = b.Buffer.Write(data[:remaining])
	}
	return len(data), nil
}

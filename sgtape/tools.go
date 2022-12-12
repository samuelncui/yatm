package sgtape

import "fmt"

const (
	LOG_PAGE_HEADER_SIZE      = 4
	LOG_PAGE_PARAMSIZE_OFFSET = 3
	LOG_PAGE_PARAM_OFFSET     = 4
)

func parseLogPage(buf []byte) (map[uint16][]byte, error) {
	pageLen := int(buf[2])<<8 + int(buf[3])

	result := make(map[uint16][]byte, pageLen)
	for i := LOG_PAGE_HEADER_SIZE; i < pageLen; {
		key := uint16(buf[i])<<8 + uint16(buf[i+1])

		valueLen := int(buf[i+LOG_PAGE_PARAMSIZE_OFFSET])
		end := i + LOG_PAGE_PARAM_OFFSET + valueLen
		if i+LOG_PAGE_PARAM_OFFSET+valueLen > len(buf) {
			return nil, fmt.Errorf("log page format unexpected, value len overflow, has= %d max= %d", i+LOG_PAGE_PARAM_OFFSET+valueLen, len(buf))
		}

		value := buf[i+LOG_PAGE_PARAM_OFFSET : end]
		copyed := make([]byte, len(value))
		copy(copyed, value)

		result[key] = copyed

		i += valueLen + LOG_PAGE_PARAM_OFFSET
	}

	return result, nil
}

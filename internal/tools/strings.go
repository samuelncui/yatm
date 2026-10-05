package tools

func UnpaddingInt8s(buf []int8) string {
	result := make([]byte, 0, len(buf))
	for _, c := range buf {
		if c == 0x00 {
			break
		}

		result = append(result, byte(c))
	}

	return string(result)
}

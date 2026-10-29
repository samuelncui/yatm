package tape

import (
	"context"
	"fmt"
)

const cartridgeMemoryMaxBytes = 8192

func readCartridgeMemory(ctx context.Context, transport Transport) ([]byte, error) {
	// Read the descriptor before allocating cartridge-supplied capacity; zero means unavailable.
	descriptor, err := query(ctx, transport, []byte{0x3c, 3, 0x10, 0, 0, 0, 0, 0, 4, 0}, 4)
	if err != nil {
		return nil, fmt.Errorf("read cartridge memory descriptor failed, %w", err)
	}
	if len(descriptor) != 4 {
		return nil, fmt.Errorf("cartridge memory descriptor has %d bytes, want 4: %w", len(descriptor), ErrMalformed)
	}
	size := int(descriptor[1])<<16 | int(descriptor[2])<<8 | int(descriptor[3])
	if size == 0 {
		return nil, nil
	}
	if size > cartridgeMemoryMaxBytes {
		return nil, fmt.Errorf("cartridge memory capacity %d exceeds %d bytes: %w",
			size, cartridgeMemoryMaxBytes, ErrMalformed)
	}

	// Keep every transfer within the existing query limit and publish only a complete image.
	data := make([]byte, size)
	cdb := []byte{0x3c, 2, 0x10, 0, 0, 0, 0, 0, 0, 0}
	for offset := 0; offset < size; {
		length := min(size-offset, MaxQueryBytes)
		cdb[3], cdb[4], cdb[5] = byte(offset>>16), byte(offset>>8), byte(offset)
		cdb[6], cdb[7], cdb[8] = byte(length>>16), byte(length>>8), byte(length)
		chunk, err := query(ctx, transport, cdb, length)
		if err != nil {
			return nil, fmt.Errorf("read cartridge memory at offset %d failed, %w", offset, err)
		}
		if len(chunk) != length {
			return nil, fmt.Errorf("cartridge memory at offset %d has %d bytes, want %d: %w",
				offset, len(chunk), length, ErrMalformed)
		}
		copy(data[offset:], chunk)
		offset += length
	}
	return data, nil
}

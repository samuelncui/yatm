package entity

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// TarStorageOrder encodes the physical Tape file and logical header offset.
func TarStorageOrder(value *TarMetadata) []byte {
	order := make([]byte, 16)
	binary.BigEndian.PutUint64(order, value.GetTapeFile())
	binary.BigEndian.PutUint64(order[8:], value.GetHeaderOffsetBytes())
	return order
}

// ValidateTarMetadata checks a durable member locator against its cartridge record bound.
func ValidateTarMetadata(value *TarMetadata, recordBytes uint32) error {
	// The label owns Tape file zero; every content header is tar-block aligned.
	if value == nil || value.TapeFile == 0 || value.HeaderOffsetBytes%512 != 0 {
		return fmt.Errorf("tar member locator has invalid Tape file or header alignment")
	}
	if (value.Header == nil) != (value.End == nil) {
		return fmt.Errorf("tar member physical positions must be supplied together")
	}
	if value.Header == nil {
		return nil
	}

	// Physical addresses describe one ordered member within the declared record size.
	header, end := value.Header, value.End
	if header.Partition != 0 || end.Partition != 0 || value.HeaderOffsetBytes < uint64(header.OffsetBytes) {
		return fmt.Errorf("tar member physical positions require partition zero and a valid containing record offset")
	}
	if recordBytes == 0 || header.OffsetBytes >= recordBytes || end.OffsetBytes >= recordBytes {
		return fmt.Errorf("tar member physical offset exceeds the record bound")
	}
	if header.Partition != end.Partition || header.LogicalObject > end.LogicalObject ||
		(header.LogicalObject == end.LogicalObject && header.OffsetBytes >= end.OffsetBytes) {
		return fmt.Errorf("tar member physical positions are not ordered in one partition")
	}
	return nil
}

// ValidateTarProfile validates public immutable format facts, including keyless recovery labels.
func ValidateTarProfile(tar *TarMediaProfile) error {
	// Initialization identity and record policy are required for independent recovery.
	if tar == nil {
		return fmt.Errorf("tar Media profile is missing")
	}
	if _, err := uuid.Parse(tar.InitializationId); err != nil {
		return fmt.Errorf("tar initialization identity is invalid, %w", err)
	}
	if err := ValidateTarRecordPolicy(tar.RecordMode, tar.RecordBytes); err != nil {
		return err
	}
	if tar.EncryptionAlgorithm != nil && strings.TrimSpace(tar.GetEncryptionAlgorithm()) == "" {
		return fmt.Errorf("tar encryption algorithm is empty")
	}
	return nil
}

// ValidateTarMediaProfile also checks the separately retained encoded encryption key.
func ValidateTarMediaProfile(value *TapeMediaProfile) error {
	// Label facts must be usable before checking Library-owned encryption material.
	tar := value.GetTar()
	if err := ValidateTarProfile(tar); err != nil {
		return err
	}

	// An observed algorithm needs its retained key; a script may leave the algorithm unknown.
	if value.Encryption == "" && tar.EncryptionAlgorithm != nil {
		return fmt.Errorf("tar encryption algorithm requires a retained key")
	}
	return nil
}

// ValidateTarRecordPolicy checks supported units; device compatibility is an attempt concern.
func ValidateTarRecordPolicy(mode TapeRecordMode, size uint32) error {
	if mode != TapeRecordMode_TAPE_RECORD_MODE_FIXED && mode != TapeRecordMode_TAPE_RECORD_MODE_VARIABLE {
		return fmt.Errorf("tar record mode must be FIXED or VARIABLE")
	}
	if size == 0 || size%512 != 0 {
		return fmt.Errorf("tar record bytes must be a positive multiple of 512")
	}
	return nil
}

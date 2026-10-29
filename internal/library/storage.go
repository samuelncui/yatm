package library

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/samuelncui/yatm/entity"
)

func validateStorageMetadata(tape *entity.TapeMediaProfile, path string, order []byte, metadata *entity.StorageMetadata, directory bool) error {
	// Derived directories have no independent physical member address.
	if directory {
		if len(order) != 0 || metadata != nil {
			return fmt.Errorf("directory Position has physical storage metadata")
		}
		return nil
	}
	if tape == nil {
		if metadata != nil || len(order) != 0 {
			return fmt.Errorf("Volume Position has sequential storage metadata")
		}
		return nil
	}
	if tape.Format != TapeFormatTarPAXV1 {
		if metadata.GetTar() != nil {
			return fmt.Errorf("LTFS Position has tar storage metadata")
		}
		return nil
	}

	// Every tar member, including an empty file, retains a valid logical locator and order key.
	tar := metadata.GetTar()
	if err := entity.ValidateTarMetadata(tar, tape.GetTar().GetRecordBytes()); err != nil {
		return err
	}
	if !strings.HasPrefix(path, fmt.Sprintf("archives/%016x/", tar.TapeFile)) {
		return fmt.Errorf("tar Position path disagrees with its Tape file namespace")
	}
	if !bytes.Equal(order, entity.TarStorageOrder(tar)) {
		return fmt.Errorf("tar Position order disagrees with its member locator")
	}
	return nil
}

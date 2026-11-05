package main

import (
	"bytes"
	"testing"
)

func TestTapeDriveOpenReportsProgressToTheGivenWriter(t *testing.T) {
	// readinfo reads the barcode from stdout, so opening progress must follow the caller's writer.
	var progress bytes.Buffer
	if _, err := TapeDriveNewFake(&progress); err != nil {
		t.Fatal(err)
	}
	if progress.String() != "Will use a fake tape drive\n" {
		t.Fatalf("progress = %q", progress.String())
	}
}

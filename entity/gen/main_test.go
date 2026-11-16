package main

import (
	"path/filepath"
	"testing"
)

func TestMixedScalarReferenceUsesNativeProtobufWrappers(t *testing.T) {
	// A typed File reference includes a scalar ID; generating methods on int64 would break Go.
	dir, err := resolveEntityDir()
	if err != nil {
		t.Fatal(err)
	}
	messages, wrappers, err := parsePbGoFile(filepath.Join(dir, "file_operation.pb.go"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := renderOneofCode(messages, wrappers)
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 0 {
		t.Fatalf("scalar reference must use native wrappers, got %s", output)
	}

	// Existing message-only dispatch helpers remain generated.
	messages, wrappers, err = parsePbGoFile(filepath.Join(dir, "storage.pb.go"))
	if err != nil {
		t.Fatal(err)
	}
	output, err = renderOneofCode(messages, wrappers)
	if err != nil || len(output) == 0 {
		t.Fatalf("message helpers missing: %s, %v", output, err)
	}
}

func TestRepeatedMessageVariantsKeepTheirDiscriminator(t *testing.T) {
	// Item and summary have the same payload type, so type-only packing would be ambiguous.
	dir, err := resolveEntityDir()
	if err != nil {
		t.Fatal(err)
	}
	messages, wrappers, err := parsePbGoFile(filepath.Join(dir, "files.pb.go"))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := messages["MeasureFilesUpdate"]; exists {
		t.Fatal("ambiguous payload generated type-only wrappers")
	}

	// Go must use protoc's discriminated oneof instead of duplicate methods.
	output, err := renderOneofCode(messages, wrappers)
	if err != nil || len(output) != 0 {
		t.Fatalf("unexpected wrapper: %s, %v", output, err)
	}
}

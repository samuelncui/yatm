package main

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestMixedScalarReferenceUsesNativeProtobufWrappers(t *testing.T) {
	// A typed File reference includes a scalar ID; generating methods on int64 would break Go and TS.
	dir, err := resolveEntityDir()
	if err != nil {
		t.Fatal(err)
	}
	messages, wrappers, err := parsePbGoFile(filepath.Join(dir, "file_operation.pb.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, render := range []func(map[string]messageOneofInfo, map[string][]wrapperInfo) ([]byte, error){renderOneofCode, renderOneofCodeTS} {
		output, err := render(messages, wrappers)
		if err != nil {
			t.Fatal(err)
		}
		if len(output) != 0 {
			t.Fatalf("scalar reference must use native wrappers, got %s", output)
		}
	}

	// Existing message-only dispatch helpers remain generated.
	messages, wrappers, err = parsePbGoFile(filepath.Join(dir, "storage.pb.go"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := renderOneofCode(messages, wrappers)
	if err != nil || len(output) == 0 {
		t.Fatalf("message helpers missing: %s, %v", output, err)
	}
}

func TestRenderOneofCodeTSUsesSingleTrailingNewline(t *testing.T) {
	// Render the smallest valid TypeScript oneof wrapper.
	output, err := renderOneofCodeTS(
		map[string]messageOneofInfo{
			"StorageMetadata": {
				messageName:  "StorageMetadata",
				oneofFieldGo: "Backend",
			},
		},
		map[string][]wrapperInfo{
			"StorageMetadata": {{
				parentMessage: "StorageMetadata",
				fieldName:     "Ltfs",
				fieldType:     "*LTFSMetadata",
			}},
		},
	)
	if err != nil {
		t.Fatalf("render TypeScript oneof code failed: %v", err)
	}

	// Generated files end in exactly one newline.
	if !bytes.HasSuffix(output, []byte("\n")) {
		t.Fatal("generated TypeScript does not end in a newline")
	}
	if bytes.HasSuffix(output, []byte("\n\n")) {
		t.Fatal("generated TypeScript ends in an extra blank line")
	}
}

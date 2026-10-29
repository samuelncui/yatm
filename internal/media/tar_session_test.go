package media

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"testing"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type tarSpecialModeInfo struct {
	tarTestInfo
	mode fs.FileMode
}

func (i tarSpecialModeInfo) Mode() fs.FileMode { return i.mode }

func TestTarSpecialPermissionMetadataRoundTrip(t *testing.T) {
	// Archive permission metadata survives standard framing and the actual selected-read endpoint.
	for _, special := range []fs.FileMode{fs.ModeSetuid, fs.ModeSetgid, fs.ModeSticky, fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky} {
		d := &tarTestDevice{records: []tarTestRecord{{mark: true}}}
		options := tarTestOptions(t, false, 4096)
		s, err := newTarTestWriteSession(d, options)
		if err != nil {
			t.Fatal(err)
		}
		ref, _ := s.Target("special", 1)
		mode := fs.FileMode(0640) | special
		w, err := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarSpecialModeInfo{tarTestInfo{name: "special", size: 1}, mode}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := w.Finish(nil); err != nil {
			t.Fatal(err)
		}
		result, err := s.Finalize(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		file := tarTestCompleted(t, s, result)[0]
		read, _ := NewTarReadSession(d, options)
		source, _ := read.Source(ReadRequest{Path: file.Path, Storage: file.Storage})
		r, err := source.Content.Open(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		metadata := tarTestMetadata(t, r)
		data, err := io.ReadAll(r)
		if err != nil || string(data) != "x" || metadata.Info.Mode() != mode {
			t.Fatalf("mode=%v expected=%v data=%q err=%v", metadata.Info.Mode(), mode, data, err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		if err := read.Finalize(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTarSettledCancellationNeverPublishesAcceptedPrefix(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		for _, tc := range []struct {
			name string
			err  error
			zero bool
		}{
			{"accepted cancel", errors.Join(context.Canceled), false},
			{"accepted deadline", errors.Join(context.DeadlineExceeded), false},
			{"unissued cancel", context.Canceled, true},
			{"unissued deadline", context.DeadlineExceeded, true},
			{"uncertain cancel", errors.Join(context.Canceled, tape.ErrUncertain), false},
			{"hardware cancel", errors.Join(context.Canceled, errors.New("hardware failure")), false},
		} {
			t.Run(map[bool]string{false: "variable", true: "fixed"}[fixed]+"/"+tc.name, func(t *testing.T) {
				// A complete earlier member does not authorize rescue commands after interrupted WRITE.
				d := &tarTestDevice{writeErrorAt: 2, writeErr: tc.err, rejectWrite: tc.zero}
				options := tarTestOptions(t, fixed, 4096)
				cleanups := 0
				options.Cleanup = func(context.Context) error { cleanups++; return nil }
				s, err := newTarTestWriteSession(d, options)
				require.NoError(t, err)
				writeTarTestMember(t, s, "first", make([]byte, 2048), nil)
				ref, err := s.Target("tail", 8192)
				require.NoError(t, err)
				w, err := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarTestInfo{name: "tail", size: 8192}})
				require.NoError(t, err)

				// Settle the submitted record, stop admission and discard every provisional publication.
				_, cause := w.Write(make([]byte, 8192))
				require.ErrorIs(t, cause, tc.err)
				require.Error(t, w.Finish(cause))
				result, err := s.Finalize(context.Background(), cause)
				require.Nil(t, result)
				require.ErrorIs(t, err, ErrFinalizeUnusable)
				require.ErrorIs(t, err, tc.err)
				require.Equal(t, 2, d.writes)
				require.Zero(t, d.marks)
				require.Equal(t, 1, cleanups)
				records := 2
				if tc.zero {
					records--
				}
				require.Len(t, d.records, records)
			})
		}
	}
}

func TestTarTrailerWarningStopsFurtherWrites(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		// Only the normal final trailer forces this record; a qualified EW still proves the member.
		d := &tarTestDevice{warningAt: 1}
		s, err := newTarTestWriteSession(d, tarTestOptions(t, fixed, 4096))
		if err != nil {
			t.Fatal(err)
		}
		writeTarTestMember(t, s, "first", []byte("saved"), nil)
		if d.writes != 0 {
			t.Fatal("fixture wrote before trailer")
		}
		result, err := s.Finalize(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(tarTestCompleted(t, s, result)) != 1 || d.writes != 1 || d.marks != 1 {
			t.Fatalf("trailer warning completion writes=%d marks=%d", d.writes, d.marks)
		}
	}
}

func TestTarFilemarkWarningCompletionGate(t *testing.T) {
	for _, override := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			err    error
			usable bool
		}{
			{"proven warning", tape.ErrEarlyWarning, true},
			{"uncertain", tape.ErrUncertain, false},
			{"mixed warning", errors.Join(tape.ErrEarlyWarning, tape.ErrUncertain), false},
			{"wrapped warning", errors.Join(tape.ErrEarlyWarning), false},
		} {
			t.Run(map[bool]string{false: "device", true: "completion callback"}[override]+"/"+tc.name, func(t *testing.T) {
				// Only the canonical result proves the provider completed the requested physical boundary.
				d := &tarTestDevice{completionErr: tc.err}
				options := tarTestOptions(t, false, 4096)
				finished := false
				options.Finish = func(context.Context) error { finished = true; return nil }
				completions := 0
				if override {
					options.Complete = func(context.Context) error { completions++; return tc.err }
				}
				s, err := newTarTestWriteSession(d, options)
				if err != nil {
					t.Fatal(err)
				}
				writeTarTestMember(t, s, "first", []byte("saved"), nil)
				result, err := s.Finalize(context.Background(), nil)
				if !tc.usable {
					if result != nil || !errors.Is(err, ErrFinalizeUnusable) || !errors.Is(err, tc.err) {
						t.Fatalf("unproven completion accepted: result=%v err=%v", result, err)
					}
				} else {
					if err != nil || len(tarTestCompleted(t, s, result)) != 1 {
						t.Fatalf("proven completion refused: %v", err)
					}
					// A completed boundary remains usable while the caller retains its no-space reason.
					require.ErrorIs(t, result.Stop, ErrTargetNoSpace)
					require.ErrorIs(t, result.Stop, tape.ErrEarlyWarning)
				}
				require.Equal(t, tc.usable, finished)
				if d.writes != 1 || override && completions != 1 || !override && tc.usable && d.marks != 1 {
					t.Fatalf("completion retried: writes=%d marks=%d callbacks=%d", d.writes, d.marks, completions)
				}
			})
		}
	}
}

func TestTarPreparedStopAdmitsNoContentOrAdditionalBoundary(t *testing.T) {
	// FORMAT can settle its initialized Media after an already completed label mark warning.
	d := &tarTestDevice{}
	options := tarTestOptions(t, false, 4096)
	options.Stop = errors.Join(ErrTargetNoSpace, tape.ErrEarlyWarning)
	completions, cleanups := 0, 0
	options.Complete = func(context.Context) error { completions++; return nil }
	options.Cleanup = func(context.Context) error { cleanups++; return nil }
	s, err := newTarTestWriteSession(d, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Target("pending", 1); err != options.Stop {
		t.Fatalf("startup stop lost: %v", err)
	}
	result, err := s.Finalize(context.Background(), options.Stop)
	if err != nil || result == nil || result.Media != options.Media || len(tarTestCompleted(t, s, result)) != 0 {
		t.Fatalf("initialized empty Media failed settlement: result=%v err=%v", result, err)
	}
	if d.writes != 0 || d.marks != 0 || completions != 1 || cleanups != 1 {
		t.Fatalf("startup stop issued content or extra boundary: writes=%d marks=%d complete=%d cleanup=%d", d.writes, d.marks, completions, cleanups)
	}
}

func TestTarIdentityLabel(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "variable", true: "fixed"}[fixed], func(t *testing.T) {
			// Identity bytes disclose record policy and public algorithm while excluding key material.
			d := &tarTestDevice{}
			options := tarTestOptions(t, fixed, 4096)
			mode := entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE
			if fixed {
				mode = entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED
			}
			algorithm := "AES-256-GCM"
			label := &TarLabel{Format: "tar_pax_v1", Barcode: "ABC123", Name: "Tape name", Profile: &entity.TarMediaProfile{
				InitializationId: "30b3fb87-6c71-4bf3-9af7-206d19e072ff", RecordMode: mode, RecordBytes: 4096, EncryptionAlgorithm: &algorithm,
			}}
			if err := WriteTarLabel(context.Background(), d, options.Mode, label); err != nil {
				t.Fatal(err)
			}
			if d.marks != 0 {
				t.Fatal("codec issued preparation owner's filemark")
			}
			if err := d.Filemarks(context.Background(), 1); err != nil {
				t.Fatal(err)
			}
			got, err := ReadTarLabel(context.Background(), d, options.Mode)
			if err != nil {
				t.Fatal(err)
			}
			if got.Format != label.Format || got.Barcode != label.Barcode || got.Name != label.Name || !proto.Equal(got.Profile, label.Profile) {
				t.Fatal("identity label changed")
			}
			if d.cursor != len(d.records) {
				t.Fatal("label reader did not settle terminating filemark")
			}

			// The complete tar terminator alone cannot qualify physical label completion.
			d.records = d.records[:len(d.records)-1]
			d.cursor = 0
			if _, err := ReadTarLabel(context.Background(), d, options.Mode); err == nil {
				t.Fatal("label without physical boundary accepted")
			}
		})
	}
}

func TestTarEmptyCompletionAndPreHeaderFailure(t *testing.T) {
	// Successful empty FORMAT completion returns its descriptor without an independent member inventory.
	d := &tarTestDevice{}
	s, err := newTarTestWriteSession(d, tarTestOptions(t, false, 4096))
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Finalize(context.Background(), nil)
	if err != nil || result == nil || result.Media == nil || result.Inventory != nil {
		t.Fatalf("empty completion unusable: %v", err)
	}
	if len(tarTestCompleted(t, s, result)) != 0 || d.marks != 1 {
		t.Fatal("empty result contains members or lacks filemark")
	}

	// A per-file rejection before its header does not discard later complete buffered members.
	d = &tarTestDevice{}
	s, err = newTarTestWriteSession(d, tarTestOptions(t, true, 16384))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := s.Target("invalid", 0)
	require.NoError(t, err)
	_, err = ref.Content.Open(context.Background(), acp.ContentMetadata{})
	require.Error(t, err)
	writeTarTestMember(t, s, "after-gap", []byte("saved"), nil)
	result, err = s.Finalize(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tarTestCompleted(t, s, result)) != 1 {
		t.Fatal("pre-header source error discarded later member")
	}
}

func TestTarCancelStopsAndCloseValidatesPadding(t *testing.T) {
	// Cancellation after a header admits neither the payload nor buffered suffix/trailer.
	d := &tarTestDevice{}
	s, err := newTarTestWriteSession(d, tarTestOptions(t, false, 4096))
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := s.Target("cancelled", 1)
	ctx, cancel := context.WithCancel(context.Background())
	w, err := ref.Content.Open(ctx, acp.ContentMetadata{Info: tarTestInfo{name: "cancelled", size: 1}})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, cause := w.Write([]byte("x"))
	_ = w.Finish(cause)

	// Abort retains the original cause without manufacturing a physical completion boundary.
	result, err := s.Finalize(context.Background(), cause)
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrFinalizeUnusable)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, d.writes)
	require.Zero(t, d.marks)

	// Padding failure remains a content-source Close error even when all expected payload bytes read.
	d = &tarTestDevice{records: []tarTestRecord{{mark: true}}}
	options := tarTestOptions(t, false, 4096)
	s, err = newTarTestWriteSession(d, options)
	if err != nil {
		t.Fatal(err)
	}
	writeTarTestMember(t, s, "file", []byte("x"), nil)
	result, err = s.Finalize(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	file := tarTestCompleted(t, s, result)[0]

	// PAX chain ends at 1536, followed by the byte payload and its padding.
	reader := tar.NewReader(bytes.NewReader(d.records[1].data))
	header, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Size != 1 {
		t.Fatal("unexpected fixture header")
	}
	memberReader := &tarRecordReader{device: d, ctx: context.Background(), buffer: make([]byte, 4096)}
	d.cursor = 1
	member, err := nextTarMember(memberReader, 1)
	if err != nil {
		t.Fatal(err)
	}
	d.records[1].data[memberReader.offset+1] = 1
	read, _ := NewTarReadSession(d, options)
	source, _ := read.Source(ReadRequest{Path: file.Path, Storage: file.Storage})
	r, err := source.Content.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err == nil {
		t.Fatal("bad padding produced successful Close")
	}
	_ = member
}

type tarMetadataTarget struct {
	bytes.Buffer
	metadata acp.ContentMetadata
	finishes int
	cause    error
	session  *tarReadSession
}

func (t *tarMetadataTarget) Open(_ context.Context, metadata acp.ContentMetadata) (acp.ContentWriter, error) {
	t.metadata = metadata
	return t, nil
}

func (t *tarMetadataTarget) Finish(cause error) error {
	t.finishes++
	t.cause = cause
	if t.session.active {
		return errors.New("target Finish preceded tar source Close")
	}
	return nil
}

func TestTarReadBatchPreservesObservedMetadataThroughACP(t *testing.T) {
	// Independent public tar framing exposes every raw PAX attribute, including managed-looking keys.
	ctx := context.Background()
	content := []byte("actual tar member content")
	attrs := map[string]string{
		"user.empty": "", "user.binary": string([]byte{0, 255, '\n'}),
		"acp.signature": "foreign cache", "user.acp.signature": "foreign namespaced cache",
	}
	device, options, members := tarPositionFixture(t, false, []int{700, 900}, []tarPositionMember{{name: "member", data: content, attrs: attrs}})
	session, err := NewTarReadSession(device, options)
	require.NoError(t, err)
	ref, err := session.Source(tarPositionRequest(t, device, members[0]))
	require.NoError(t, err)
	batch, err := NewReadBatch([]int64{1})
	require.NoError(t, err)
	source, err := batch.WrapSource(1, ref.Content)
	require.NoError(t, err)
	target := &tarMetadataTarget{session: session.(*tarReadSession)}

	// ACP observes the wrapped file, filters its ordinary snapshot, and hashes the actual bytes.
	var results []acp.Result
	engine, err := acp.NewStream(ctx, func(values []acp.Result) error { results = append(results, values...); return nil },
		acp.WithHashPolicy(acp.HashRead), acp.SetFromDevice(DeviceOptions(AccessSequential)...), acp.SetToDevice(DeviceOptions(AccessSequential)...))
	require.NoError(t, err)
	require.NoError(t, engine.Submit(&tarPipelineItem{source: members[0].name, content: source, target: target}))
	require.NoError(t, engine.Close())
	require.NoError(t, engine.Wait())
	require.Len(t, results, 1)
	require.NoError(t, results[0].Err)
	require.Len(t, results[0].Targets, 1)
	require.NoError(t, results[0].Targets[0].Err)
	hash := sha256.Sum256(content)
	require.Equal(t, hash[:], results[0].SHA256)
	require.Equal(t, content, target.Bytes())
	require.EqualValues(t, len(content), target.metadata.Info.Size())
	require.EqualValues(t, 0640, target.metadata.Info.Mode().Perm())
	require.EqualValues(t, 1000, target.metadata.Info.ModTime().Unix())
	require.Equal(t, map[string][]byte{"user.empty": {}, "user.binary": {0, 255, '\n'}}, target.metadata.Xattrs)

	// Successful settlement follows real tar payload/padding Close and the sole target Finish.
	require.Equal(t, 1, target.finishes)
	require.NoError(t, target.cause)
	require.False(t, target.session.active)
	require.Equal(t, members[0].end, target.session.records.offset)
	require.NoError(t, batch.WaitSources(ctx, nil))
	require.NoError(t, session.Finalize(ctx, nil))
}

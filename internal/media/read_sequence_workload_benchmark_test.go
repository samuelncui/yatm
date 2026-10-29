package media

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/samuelncui/yatm/internal/media/tape"
	"google.golang.org/protobuf/proto"
)

type restoreWorkload struct {
	TotalBytes uint64 `json:"total_bytes"`
	Files      []struct {
		Path string `json:"path"`
		Size uint64 `json:"size"`
	} `json:"files"`
	Content struct {
		Mode    int64 `json:"mode"`
		MtimeNS int64 `json:"mtime_ns"`
	} `json:"content"`
}

func loadRestoreWorkload(tb testing.TB) ([]readplan.Span, uint64, uint64) {
	tb.Helper()
	// Both the physical generator and this forecast consume the same frozen population and selection.
	root := filepath.Join("..", "..", "e2e", "fixtures", "restore-1p5tb-v1")
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		tb.Fatal(err)
	}
	var workload restoreWorkload
	if err := json.Unmarshal(data, &workload); err != nil {
		tb.Fatal(err)
	}
	selection, err := os.ReadFile(filepath.Join(root, "selection.txt"))
	if err != nil {
		tb.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != "4a5c8d75e35613d6c0f4df2b5516e52031aed6caac46d5de59052ab4099ba340" ||
		fmt.Sprintf("%x", sha256.Sum256(selection)) != "f73664ae8d3fa900df2fe25126d887f2975ceceb65dd4700061046d77e3fdd05" {
		tb.Fatal("canonical mixed-size population or selection changed")
	}
	wanted := make(map[string]bool)
	for _, name := range strings.Fields(string(selection)) {
		if wanted[name] {
			tb.Fatalf("duplicate frozen selection %q", name)
		}
		wanted[name] = true
	}
	if len(workload.Files) != 3000 || len(wanted) != 600 || workload.TotalBytes != 1_500_000_000_000 {
		tb.Fatal("frozen workload must contain 3000 mixed files / 1.5 TB and 600 selected paths")
	}

	// Ordinary Archive orders target paths, independently of creation or manifest serialization order.
	sort.Slice(workload.Files, func(i, j int) bool { return workload.Files[i].Path < workload.Files[j].Path })

	// Calculate real PAX header sizes without creating or reading the 1.5 TB payload.
	const recordBytes = uint64(512 << 10)
	cursor, total, selected := 2*recordBytes, uint64(0), uint64(0)
	spans := make([]readplan.Span, 0, len(wanted))
	for _, file := range workload.Files {
		var header bytes.Buffer
		if err := tar.NewWriter(&header).WriteHeader(&tar.Header{
			Name: tarPrefix(1) + "/" + file.Path, Size: int64(file.Size), Mode: workload.Content.Mode,
			ModTime: time.Unix(0, workload.Content.MtimeNS), Format: tar.FormatPAX,
		}); err != nil {
			tb.Fatal(err)
		}
		start := cursor
		cursor += uint64(header.Len()) + (file.Size+511)/512*512
		total += file.Size
		if !wanted[file.Path] {
			continue
		}
		spans = append(spans, readplan.Span{ID: int64(len(spans) + 1), Ranges: []readplan.Range{{
			Start:  &entity.TapeBytePosition{LogicalObject: start / recordBytes, OffsetBytes: uint32(start % recordBytes)},
			End:    &entity.TapeBytePosition{LogicalObject: cursor / recordBytes, OffsetBytes: uint32(cursor % recordBytes)},
			Stream: tarPrefix(1),
		}}})
		selected += file.Size
		delete(wanted, file.Path)
	}
	if total != workload.TotalBytes || len(wanted) != 0 || len(spans) != 600 {
		tb.Fatal("population bytes or selected path coverage differs from the frozen workload")
	}
	tb.Logf("manifest_sha256=%x selection_sha256=%x payload_bytes=%d selected_bytes=%d",
		sha256.Sum256(data), sha256.Sum256(selection), total, selected)
	return spans, (cursor+recordBytes-1)/recordBytes + 1, selected
}

func restoreWorkloadProvider(tb testing.TB, objects uint64) *tarReadOrderProvider {
	tb.Helper()
	// Nominal uncompressed 80-wrap geometry is an assumption, not a measured map for this new corpus.
	// Timing inputs freeze the current HP5-derived prior; see the full-cartridge study in tar-tape.md.
	profile := ReadModelProfile{ID: "restore-1p5tb-v1-nominal-lto5", SnapshotValid: func() bool { return true },
		Boundary: ReadEndpointExclusive, BOT: map[uint32]uint64{0: 0}, FullWrapSeconds: proto.Float64(92.69041838827268),
		Coefficients: ReadModelCoefficients{Base: 3.223942631933043, Wrap: 6.252332019046645,
			Band: -.8279789490339896, LandingZone: .6469527733076674,
			Direction: 1.2685235350999264, StepBack: 5.965091877336342}}
	for wrap := range 80 {
		direction := ReadWrapForward
		if wrap%2 != 0 {
			direction = ReadWrapReverse
		}
		profile.Wraps = append(profile.Wraps, ReadModelWrap{Wrap: uint32(wrap), Band: uint32(wrap / 20),
			Direction: direction, Full: true, Endpoint: proto.Uint64(uint64(wrap+1) * objects / 80)})
	}
	model, err := NewReadCostModel(profile)
	if err != nil {
		tb.Fatal(err)
	}
	session := &tarReadSession{options: TarSessionOptions{
		Mode: tape.RecordMode{MaximumBytes: 512 << 10}, ReadModel: model,
		ReadTiming: &entity.TapeReadTiming{BytesPerSecond: proto.Float64(140e6),
			PostLocateReadOverheadSeconds: proto.Float64(.011400702694972826)},
		ReadCompletion: &ReadCompletion{FullSpanSeconds: 96, FixedSeconds: 19},
		Finish:         func(context.Context) error { panic("forecast must not issue device I/O") },
	}, navigator: readScenarioNavigator{}, file: 1, fileKnown: true,
		records: &tarRecordReader{nextObject: 2, nextKnown: true}}
	return &tarReadOrderProvider{session: session}
}

func TestRestoreWorkloadUsesFrozenMixedSizes(t *testing.T) {
	// Fixture identity and arithmetic are shared with physical generation, rather than resampled by the benchmark.
	spans, objects, selected := loadRestoreWorkload(t)
	if selected == 0 || selected >= 1_500_000_000_000 {
		t.Fatal("selected bytes must be measured from the fixed subset")
	}
	provider := restoreWorkloadProvider(t, objects)
	state, known, err := provider.Current(context.Background())
	if err != nil || !known || state.Position.GetLogicalObject() != 2 {
		t.Fatalf("prepared initial state unavailable: %v %t %v", state, known, err)
	}
	for _, span := range spans {
		transition, known := provider.Evaluate(state, span.Ranges[0])
		if !known {
			t.Fatalf("complete range unavailable for selected member %d", span.ID)
		}
		state = transition.Next
	}
	if finish, known := provider.Finish(state); !known || finish <= 0 {
		t.Fatal("full workload must include the final unload quote")
	}
}

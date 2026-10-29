package media

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"iter"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/samuelncui/yatm/internal/media/tape"
	"google.golang.org/protobuf/proto"
)

const readScenarioVersion = "restore-scenarios-v1"
const readScenarioRecord = uint64(512 << 10)
const readScenarioPlanningBudget = 300 * time.Second

var readScenarioSeeds = map[string][]uint64{
	"development": {11, 23, 37, 53, 71},
	"acceptance":  {101, 131, 167, 199, 233},
}

type readScenarioNavigator struct{}

func (readScenarioNavigator) AddressBits() uint32 { return 64 }
func (readScenarioNavigator) Position(context.Context) (tape.Position, error) {
	panic("metadata scenario issued Position")
}
func (readScenarioNavigator) Locate(context.Context, uint32, uint64) error {
	panic("metadata scenario issued Locate")
}

func readScenarioInput(spans []readplan.Span) iter.Seq2[readplan.Span, error] {
	return func(yield func(readplan.Span, error) bool) {
		for _, span := range spans {
			if !yield(span, nil) {
				return
			}
		}
	}
}

type readScenarioModel struct {
	Boundary          ReadEndpointConvention `json:"boundary"`
	BOT               map[uint32]uint64      `json:"bot"`
	Wraps             []ReadModelWrap        `json:"wraps"`
	FullWrapSeconds   float64                `json:"fullWrapSeconds"`
	UncertaintyNS     int64                  `json:"uncertaintyNS"`
	Coefficients      ReadModelCoefficients  `json:"coefficients"`
	BytesPerSecond    float64                `json:"bytesPerSecond"`
	PostLocateSeconds float64                `json:"postLocateSeconds"`
	Completion        ReadCompletion         `json:"completion"`
	DataEnd           uint64                 `json:"dataEnd"`
}

type readScenario struct {
	ID, Family, Backend, Split string
	Seed                       uint64
	Spans                      []readplan.Span
	Model                      readScenarioModel
	PopulationFiles            int
	PopulationBytes            uint64
	SelectedBytes              uint64
	ReferenceRoute             []int64
	FileIDs                    map[int64]int64
	ReferenceNS                int64
	PlanningBudgetNS           int64
	NoModel                    bool
	IncompleteMap              bool
	NotApplicable              string
}

func readScenarioModelHash(tb testing.TB, scenario readScenario) string {
	tb.Helper()
	// LTFS terminal policy changes edge costs even when every stored numerical parameter is unchanged.
	var value any = scenario.Model
	if scenario.Backend == "ltfs" {
		value = struct {
			CompletionPolicy string            `json:"completionPolicy"`
			Model            readScenarioModel `json:"model"`
		}{CompletionPolicy: "no-index-rewrite-unload", Model: scenario.Model}
	}
	data, err := json.Marshal(value)
	if err != nil {
		tb.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

type readScenarioRecipe struct {
	Family, Name           string
	Count, Groups, Percent int
}

func readScenarioRecipes() []readScenarioRecipe {
	// These explicit combinations cover boundaries without constructing a Cartesian product.
	var recipes []readScenarioRecipe
	for _, percent := range []int{1, 5, 20, 50, 90, 100} {
		recipes = append(recipes, readScenarioRecipe{"uniform", fmt.Sprintf("select%d", percent), 400, 0, percent})
	}
	for _, groups := range []int{1, 10, 22, 31, 32, 33, 64} {
		recipes = append(recipes, readScenarioRecipe{"clusters", fmt.Sprintf("groups%d", groups), groups * 4, groups, 100})
	}
	for family, names := range map[string][]string{
		"sizes":     {"equal", "small", "bimodal", "long-tail"},
		"gaps":      {"continuous", "short", "long", "alternating", "threshold-below", "threshold-above"},
		"layout":    {"wrap-band", "ends", "partial-tail"},
		"files":     {"multi-extent", "cross-record", "shared-record", "append"},
		"cost":      {"positioning", "longitudinal", "reading"},
		"difficult": {"uneven-groups", "near-equal", "uncertain", "no-model", "missing-map", "exact-small"},
	} {
		for _, name := range names {
			count := 24
			if name == "exact-small" {
				count = 7
			}
			if strings.HasPrefix(name, "threshold-") {
				count = 2
			}
			if name == "uneven-groups" {
				count = 64
			}
			recipes = append(recipes, readScenarioRecipe{family, name, count, 0, 100})
			if family == "sizes" {
				recipes = append(recipes, readScenarioRecipe{family, name + "-select20", 120, 0, 20})
			}
		}
	}
	sort.Slice(recipes, func(i, j int) bool {
		return recipes[i].Family+"/"+recipes[i].Name < recipes[j].Family+"/"+recipes[j].Name
	})
	return recipes
}

func readScenarioPosition(value uint64, partition uint32) *entity.TapeBytePosition {
	return &entity.TapeBytePosition{Partition: partition, LogicalObject: value / readScenarioRecord, OffsetBytes: uint32(value % readScenarioRecord)}
}

func makeReadScenario(recipe readScenarioRecipe, backend, split string, seed uint64) readScenario {
	// The versioned PCG seed and complete generated input are both retained by the quality report.
	random := rand.New(rand.NewPCG(seed, 0x7a746d01))
	scenario := readScenario{ID: fmt.Sprintf("%s/%s/%s/%s/%d", backend, recipe.Family, recipe.Name, split, seed),
		Family: recipe.Family, Backend: backend, Split: split, Seed: seed, PopulationFiles: recipe.Count, PlanningBudgetNS: int64(readScenarioPlanningBudget)}
	partition, stream := uint32(0), tarPrefix(1)
	if backend == "ltfs" {
		partition, stream = 1, "ltfs"
	}
	selected := make([]bool, recipe.Count)
	for _, index := range random.Perm(recipe.Count)[:max(1, recipe.Count*recipe.Percent/100)] {
		selected[index] = true
	}
	cursor := 2 * readScenarioRecord
	groups := recipe.Groups
	if recipe.Name == "uneven-groups" {
		groups = 10
	}
	for index := 0; index < recipe.Count; index++ {
		// Different payload sizes also exercise exact byte offsets and partial final records.
		size := uint64(32+random.IntN(993)) * readScenarioRecord
		switch strings.TrimSuffix(recipe.Name, "-select20") {
		case "equal":
			size = 512 * readScenarioRecord
		case "small", "shared-record":
			size = uint64(256 + random.IntN(8192))
		case "bimodal":
			if index%2 == 0 {
				size = 4096
			} else {
				size = 4096 * readScenarioRecord
			}
		case "long-tail":
			size = uint64(math.Exp(random.Float64()*10)) * 1024
		case "cross-record":
			size = readScenarioRecord + uint64(1+random.IntN(511))
		}
		gap := uint64(0)
		switch recipe.Family {
		case "gaps":
			switch recipe.Name {
			case "short":
				gap = 8 * readScenarioRecord
			case "long":
				gap = 15000 * readScenarioRecord
			case "alternating":
				if index%2 == 0 {
					gap = 15000 * readScenarioRecord
				} else {
					gap = 8 * readScenarioRecord
				}
			case "threshold-below":
				gap = 800 * readScenarioRecord
			case "threshold-above":
				gap = 900 * readScenarioRecord
			}
		case "cost", "layout", "difficult":
			if recipe.Name != "uneven-groups" {
				gap = uint64(2000+random.IntN(30000)) * readScenarioRecord
			}
		}
		groupAt := func(at int) int {
			if recipe.Name == "uneven-groups" {
				return int(math.Sqrt(float64(at)/float64(recipe.Count)) * float64(groups))
			}
			return at * groups / recipe.Count
		}
		if groups > 0 && index > 0 && groupAt(index) != groupAt(index-1) {
			gap = uint64(1_000_000+random.IntN(10000)) * readScenarioRecord
		}
		if recipe.Name == "ends" && index == recipe.Count/2 {
			gap = 1_000_000 * readScenarioRecord
		}
		if recipe.Name == "near-equal" {
			gap = readScenarioRecord
		}
		cursor += gap
		if recipe.Name != "shared-record" && recipe.Name != "cross-record" {
			cursor = (cursor + readScenarioRecord - 1) / readScenarioRecord * readScenarioRecord
		}
		if recipe.Name == "append" && index > 0 && index%6 == 0 {
			cursor = (cursor+readScenarioRecord-1)/readScenarioRecord*readScenarioRecord + 3*readScenarioRecord
			if backend == "tar" {
				stream = tarPrefix(uint64(index/6 + 1))
			}
		}
		ranges := []readplan.Range{{Start: readScenarioPosition(cursor, partition), End: readScenarioPosition(cursor+size, partition), Stream: stream}}
		cursor += size
		if recipe.Name == "multi-extent" {
			// Fragmented LTFS files retain file-offset order even when their physical extents go backwards.
			ranges = []readplan.Range{{Start: readScenarioPosition(cursor+200*readScenarioRecord, partition), End: readScenarioPosition(cursor+220*readScenarioRecord, partition), Stream: stream}, ranges[0]}
			size += 20 * readScenarioRecord
			cursor += 221 * readScenarioRecord
		}
		scenario.PopulationBytes += size
		if selected[index] {
			scenario.Spans = append(scenario.Spans, readplan.Span{ID: int64(index + 1), Ranges: ranges})
			scenario.SelectedBytes += size
		}
	}

	// The generic layout is deliberately synthetic; timing and geometry are frozen, not fitted to these cases.
	end := max(uint64(800), (cursor+readScenarioRecord-1)/readScenarioRecord+10)
	if strings.HasPrefix(recipe.Name, "threshold-") {
		end = 80_000_000
	}
	scenario.Model = readScenarioModel{Boundary: ReadEndpointExclusive, BOT: map[uint32]uint64{partition: 0},
		FullWrapSeconds: 92.69041838827268, UncertaintyNS: 9_121_917_392,
		Coefficients: ReadModelCoefficients{Base: 3.223942631933043, Wrap: 6.252332019046645, Band: -.8279789490339896,
			LandingZone: .6469527733076674, Direction: 1.2685235350999264, StepBack: 5.965091877336342},
		BytesPerSecond: 140e6, PostLocateSeconds: .011400702694972826,
		Completion: ReadCompletion{FullSpanSeconds: 96, FixedSeconds: 19}, DataEnd: end}
	for wrap := 0; wrap < 80; wrap++ {
		direction := ReadWrapForward
		if wrap%2 != 0 {
			direction = ReadWrapReverse
		}
		scenario.Model.Wraps = append(scenario.Model.Wraps, ReadModelWrap{Partition: partition, Wrap: uint32(wrap), Band: uint32(wrap / 20), Direction: direction, Full: true, Endpoint: proto.Uint64(uint64(wrap+1) * end / 80), EndpointUncertaintyObjects: proto.Uint64(0)})
	}
	if recipe.Name == "partial-tail" {
		last := &scenario.Model.Wraps[len(scenario.Model.Wraps)-1]
		last.Full, last.EstimatedSpanObjects = false, proto.Uint64(2*(end-*scenario.Model.Wraps[78].Endpoint))
	}
	switch recipe.Name {
	case "positioning":
		scenario.Model.Coefficients.Base *= 4
	case "longitudinal":
		scenario.Model.FullWrapSeconds *= 4
	case "reading":
		scenario.Model.BytesPerSecond /= 4
	case "uncertain":
		scenario.Model.UncertaintyNS *= 10
	case "no-model":
		scenario.NoModel = true
	case "missing-map":
		scenario.IncompleteMap = true
		position := scenario.Spans[len(scenario.Spans)/2].Ranges[0].Start.LogicalObject
		for i, wrap := range scenario.Model.Wraps {
			if *wrap.Endpoint > position {
				scenario.Model.Wraps[i].Endpoint = nil
				break
			}
		}
	}
	if recipe.Name == "multi-extent" && backend == "tar" {
		scenario.NotApplicable = "tar members do not have LTFS-style disjoint extents"
	}
	return scenario
}

func (s readScenario) provider(tb testing.TB) ReadOrderProvider {
	tb.Helper()
	if s.NoModel {
		return nil
	}
	// Production evaluators receive the same immutable metadata on both source revisions.
	m, budget := s.Model, time.Duration(s.PlanningBudgetNS)
	model, err := NewReadCostModel(ReadModelProfile{ID: readScenarioVersion, SnapshotValid: func() bool { return true },
		Boundary: m.Boundary, BOT: m.BOT, Wraps: m.Wraps, FullWrapSeconds: proto.Float64(m.FullWrapSeconds),
		Coefficients: m.Coefficients})
	if err != nil {
		tb.Fatal(err)
	}
	timing := &entity.TapeReadTiming{BytesPerSecond: proto.Float64(m.BytesPerSecond), PostLocateReadOverheadSeconds: proto.Float64(m.PostLocateSeconds),
		FilemarkOverheadSeconds: proto.Float64(.001)}
	if s.Backend == "ltfs" {
		provider, reason := NewLTFSReadOrderProvider(LTFSReadOptions{BlockBytes: uint32(readScenarioRecord), Compression: proto.Bool(false),
			Partitions: map[string]uint32{"b": 1}, Model: model, Timing: timing, Completion: &m.Completion,
			DataEnd: &entity.TapeBytePosition{Partition: 1, LogicalObject: m.DataEnd}, PlanningBudget: budget})
		if provider == nil {
			tb.Fatal(reason)
		}
		return provider
	}
	session := &tarReadSession{options: TarSessionOptions{Mode: tape.RecordMode{MaximumBytes: int(readScenarioRecord)}, ReadModel: model,
		ReadTiming: timing, ReadCompletion: &m.Completion, ReadPlanningBudget: budget,
		Finish: func(context.Context) error { panic("scenario must never issue physical I/O") }}, navigator: readScenarioNavigator{},
		file: 1, fileKnown: true, records: &tarRecordReader{nextObject: 2, nextKnown: true}}
	return &tarReadOrderProvider{session: session}
}

func loadReadScenario308(tb testing.TB) readScenario {
	tb.Helper()
	data, err := os.ReadFile("testdata/restore/ltfs308-v1.json")
	if err != nil {
		tb.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != "08f8dd019a95916b8a68f1c5e9255343d59452d5cf53c1f445ae6717521a39af" {
		tb.Fatal("frozen 308 input changed")
	}
	var fixture struct {
		Model          readScenarioModel
		Spans          []readplan.Span
		SelectedBytes  uint64
		ReferenceRoute []int64
		FileIDs        map[int64]int64
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		tb.Fatal(err)
	}

	// Keep the historical fixture untouched; its fixed route is a feasible reference under today's evaluator.
	scenario := readScenario{ID: "ltfs/frozen/mixed308", Family: "frozen", Backend: "ltfs", Split: "fixed", Model: fixture.Model, PlanningBudgetNS: int64(readScenarioPlanningBudget),
		Spans: fixture.Spans, SelectedBytes: fixture.SelectedBytes, PopulationFiles: 308, PopulationBytes: fixture.SelectedBytes,
		ReferenceRoute: fixture.ReferenceRoute, FileIDs: fixture.FileIDs}
	reference, err := scoreReadScenario(scenario, scenario.provider(tb), scenario.ReferenceRoute)
	if err != nil {
		tb.Fatal(err)
	}
	scenario.ReferenceNS = reference.EstimateNS
	return scenario
}

func loadReadScenarioWorkload(tb testing.TB, backend string) readScenario {
	tb.Helper()
	spans, end, selected := loadRestoreWorkload(tb)
	// Reuse the authoritative 1.5 TB file population and fixed 20% selection, including real PAX header bytes.
	scenario := makeReadScenario(readScenarioRecipe{"uniform", "select20", 400, 0, 20}, backend, "fixed", 11)
	scenario.ID, scenario.Family, scenario.Split, scenario.Seed = backend+"/frozen/restore1p5TB", "frozen", "fixed", 0
	scenario.PopulationFiles, scenario.PopulationBytes, scenario.SelectedBytes = 3000, 1_500_000_000_000, selected
	scenario.Spans, scenario.Model.DataEnd = spans, end
	for i := range scenario.Model.Wraps {
		scenario.Model.Wraps[i].Endpoint = proto.Uint64(uint64(i+1) * end / 80)
	}
	if backend == "ltfs" {
		for i := range scenario.Spans {
			for j := range scenario.Spans[i].Ranges {
				r := &scenario.Spans[i].Ranges[j]
				r.Start.Partition, r.End.Partition, r.Stream = 1, 1, "ltfs"
			}
		}
	}
	return scenario
}

func readScenarioRegressions(tb testing.TB) []readScenario {
	tb.Helper()
	// Reuse the exact fixed cases and original recipes/seeds that exposed the sensitivity failures.
	scenarios := []readScenario{loadReadScenario308(tb), loadReadScenarioWorkload(tb, "tar"), loadReadScenarioWorkload(tb, "ltfs")}
	selected := map[string]struct {
		split string
		seed  uint64
	}{
		"uniform/select5":          {"development", 53},
		"layout/partial-tail":      {"acceptance", 131},
		"sizes/long-tail-select20": {"acceptance", 167},
	}
	for _, recipe := range readScenarioRecipes() {
		if item, exists := selected[recipe.Family+"/"+recipe.Name]; exists {
			scenarios = append(scenarios, makeReadScenario(recipe, "ltfs", item.split, item.seed))
		}
	}
	if len(scenarios) != 6 {
		tb.Fatal("regressions profile must contain the three fixed and three original sensitivity cases")
	}
	return scenarios
}

func TestReadSequenceScenarioRegressionInventory(t *testing.T) {
	// Focused acceptance must preserve these exact inputs rather than quietly substituting another seed.
	wanted := map[string]bool{
		"ltfs/frozen/mixed308":                         true,
		"tar/frozen/restore1p5TB":                      true,
		"ltfs/frozen/restore1p5TB":                     true,
		"ltfs/uniform/select5/development/53":          true,
		"ltfs/layout/partial-tail/acceptance/131":      true,
		"ltfs/sizes/long-tail-select20/acceptance/167": true,
	}
	for _, scenario := range readScenarioRegressions(t) {
		if !wanted[scenario.ID] {
			t.Fatalf("unexpected or duplicate regression input %s", scenario.ID)
		}
		delete(wanted, scenario.ID)
	}
	if len(wanted) != 0 {
		t.Fatalf("missing regression inputs: %v", wanted)
	}
}

func TestReadSequenceScenarioDiagnosticCountAvailability(t *testing.T) {
	// An unavailable old-source counter differs from a measured zero on a new source.
	if count := readScenarioDiagnosticCount(struct{}{}, "evaluations"); count != nil {
		t.Fatal("missing diagnostic became a measured count")
	}
	for _, evaluations := range []uint64{0, 17} {
		count := readScenarioDiagnosticCount(struct{ evaluations uint64 }{evaluations}, "evaluations")
		if count == nil || *count != evaluations {
			t.Fatalf("diagnostic count = %v, want %d", count, evaluations)
		}
	}
}

package media

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/samuelncui/yatm/internal/resource"
)

type readScenarioSensitivity struct {
	Factor     string  `json:"factor"`
	Multiplier float64 `json:"multiplier"`
	BaselineNS int64   `json:"baselineNS"`
	RetainedNS int64   `json:"retainedNS"`
}

type readScenarioPassSummary struct {
	Number     uint64           `json:"number"`
	Before     readScenarioCost `json:"before"`
	After      readScenarioCost `json:"after"`
	Proposals  uint64           `json:"proposals"`
	Accepted   uint64           `json:"accepted"`
	Reused     uint64           `json:"reused"`
	Complete   bool             `json:"complete"`
	Descending bool             `json:"descending"`
	Moves      [][3]int64       `json:"moves"`
}

type readScenarioSearchDiagnostics struct {
	Passes           uint64                    `json:"passes"`
	Completed        uint64                    `json:"completed"`
	Proposals        uint64                    `json:"proposals"`
	Accepted         uint64                    `json:"accepted"`
	Reused           uint64                    `json:"reused"`
	Reconnections    *uint64                   `json:"reconnections"`
	FullScores       *uint64                   `json:"fullScores"`
	TotalEvaluations *uint64                   `json:"totalEvaluations"`
	IndexNS          *int64                    `json:"indexNS"`
	SearchNS         *int64                    `json:"searchNS"`
	FullScoreNS      *int64                    `json:"fullScoreNS"`
	CommitNS         *int64                    `json:"commitNS"`
	Recent           []readScenarioPassSummary `json:"recent"`
}

type readScenarioResult struct {
	ID                  string                        `json:"id"`
	Family              string                        `json:"family"`
	Backend             string                        `json:"backend"`
	Split               string                        `json:"split"`
	Seed                uint64                        `json:"seed"`
	InputSHA256         string                        `json:"inputSHA256"`
	ModelSHA256         string                        `json:"modelSHA256"`
	Input               readScenario                  `json:"input"`
	Status              string                        `json:"status"`
	AdmissionPolicy     string                        `json:"admissionPolicy"`
	Diagnostics         readScenarioSearchDiagnostics `json:"diagnostics"`
	Baseline            *readScenarioCost             `json:"baseline"`
	Retained            *readScenarioCost             `json:"retained"`
	Best                *readScenarioCost             `json:"best"`
	BestRouteKnown      bool                          `json:"bestRouteKnown"`
	BestRoute           []int64                       `json:"bestRoute,omitempty"`
	PlannedNS           int64                         `json:"plannedNS"`
	RoutePlusPlanningNS int64                         `json:"routePlusPlanningNS"`
	IndexNS             int64                         `json:"indexNS"`
	SearchNS            *int64                        `json:"searchNS"`
	FullScoreNS         *int64                        `json:"fullScoreNS"`
	CommitNS            *int64                        `json:"commitNS"`
	ExhaustiveRuns      *int64                        `json:"exhaustiveRuns"`
	IndexedRuns         *int64                        `json:"indexedRuns"`
	Evaluations         uint64                        `json:"evaluations"`
	AllocatedBytes      uint64                        `json:"allocatedBytes"`
	TemporaryBytes      int64                         `json:"temporaryBytes"`
	Route               []int64                       `json:"route"`
	FileRoute           []int64                       `json:"fileRoute,omitempty"`
	LogicalRoute        []int64                       `json:"logicalRoute"`
	Termination         string                        `json:"termination"`
	PlannerReason       string                        `json:"plannerReason"`
	PlanningBudgetNS    int64                         `json:"planningBudgetNS"`
	Planned             bool                          `json:"planned"`
	SelectedFiles       int                           `json:"selectedFiles"`
	SelectedBytes       uint64                        `json:"selectedBytes"`
	PopulationFiles     int                           `json:"populationFiles"`
	PopulationBytes     uint64                        `json:"populationBytes"`
	Exact               *readScenarioExact            `json:"exact,omitempty"`
	ReferenceNS         int64                         `json:"referenceNS,omitempty"`
	Sensitivity         []readScenarioSensitivity     `json:"sensitivity,omitempty"`
}

type readScenarioObservedProvider struct {
	ReadOrderProvider
	started, indexed time.Time
	evaluations      uint64
}

func (p *readScenarioObservedProvider) Current(ctx context.Context) (readplan.State, bool, error) {
	if p.indexed.IsZero() {
		p.indexed = time.Now()
	}
	return p.ReadOrderProvider.Current(ctx)
}
func (p *readScenarioObservedProvider) Evaluate(state readplan.State, interval readplan.Range) (readplan.Transition, bool) {
	p.evaluations++
	return p.ReadOrderProvider.Evaluate(state, interval)
}
func (p *readScenarioObservedProvider) ReadPlanningBudget() time.Duration {
	return p.ReadOrderProvider.ReadPlanningBudget()
}
func (p *readScenarioObservedProvider) ReadIndex(position *entity.TapeBytePosition) (readplan.Index, bool) {
	return p.ReadOrderProvider.ReadIndex(position)
}
func (p *readScenarioObservedProvider) CanonicalReadState(span readplan.Span) (readplan.State, bool) {
	return p.ReadOrderProvider.CanonicalReadState(span)
}
func (p *readScenarioObservedProvider) FixedFirstReadState(span readplan.Span) (readplan.State, bool) {
	return p.ReadOrderProvider.FixedFirstReadState(span)
}
func (p *readScenarioObservedProvider) Finish(state readplan.State) (time.Duration, bool) {
	return p.ReadOrderProvider.Finish(state)
}

func (p *readScenarioObservedProvider) ReadCostVariants() ([6]ReadOrderProvider, bool) {
	// The anonymous capability keeps the common harness compilable with the frozen old source.
	if capability, ok := p.ReadOrderProvider.(interface {
		ReadCostVariants() ([6]ReadOrderProvider, bool)
	}); ok {
		return capability.ReadCostVariants()
	}
	return [6]ReadOrderProvider{}, false
}

func readScenarioDiagnosticTime(diagnostic any, name string) *int64 {
	// Optional diagnostics keep this exact harness source-compatible with the frozen old implementation.
	value := reflect.ValueOf(diagnostic).FieldByName(name)
	if !value.IsValid() || value.Kind() != reflect.Int64 && value.Kind() != reflect.Int {
		return nil
	}
	ns := value.Int()
	return &ns
}

func readScenarioDiagnosticCount(diagnostic any, name string) *uint64 {
	// New counters remain unknown on the frozen old source instead of implying a measured zero.
	value := reflect.ValueOf(diagnostic).FieldByName(name)
	if !value.IsValid() || value.Kind() != reflect.Uint64 {
		return nil
	}
	count := value.Uint()
	return &count
}

// readScenarioDiagnostics is empty: the production planner retains no search diagnostics.
func readScenarioDiagnostics(*readplan.Sequence) readScenarioSearchDiagnostics {
	return readScenarioSearchDiagnostics{}
}

// readScenarioBest is unknown without planner diagnostics; callers use the replayed retained route.
func readScenarioBest(*readplan.Sequence) readScenarioCost { return readScenarioCost{} }

func readScenarioResultInput(tb testing.TB, scenario readScenario) readScenarioResult {
	tb.Helper()
	// Preserve complete reproducible input even when execution never starts.
	input, err := json.Marshal(scenario)
	if err != nil {
		tb.Fatal(err)
	}
	return readScenarioResult{ID: scenario.ID, Family: scenario.Family, Backend: scenario.Backend, Split: scenario.Split, Seed: scenario.Seed,
		Input: scenario, InputSHA256: fmt.Sprintf("%x", sha256.Sum256(input)), ModelSHA256: readScenarioModelHash(tb, scenario),
		Status: "not-run", LogicalRoute: scenario.logical(), SelectedFiles: len(scenario.Spans), SelectedBytes: scenario.SelectedBytes,
		PopulationFiles: scenario.PopulationFiles, PopulationBytes: scenario.PopulationBytes, ReferenceNS: scenario.ReferenceNS, PlanningBudgetNS: scenario.PlanningBudgetNS}
}

func runReadScenario(tb testing.TB, scenario readScenario, exact bool, result *readScenarioResult) {
	tb.Helper()
	// Failed subtests retain their complete input and every route page collected before the failure.
	result.Status = "failed"
	defer func() {
		if tb.Failed() {
			result.Status = "failed"
		}
	}()
	if scenario.NotApplicable != "" {
		result.Status, result.Termination, result.Route = "skipped", scenario.NotApplicable, scenario.logical()
		return
	}

	// Measure the same complete planner operation without charging later reference replay to its time.
	provider := scenario.provider(tb)
	if provider == nil {
		// Without a model the caller keeps its manifest order and never builds a planning index.
		result.Status, result.Termination, result.Route = "no-model", "no read-cost provider", scenario.logical()
		return
	}
	var observed *readScenarioObservedProvider
	if provider != nil {
		observed = &readScenarioObservedProvider{ReadOrderProvider: provider}
		provider = observed
	}
	root := tb.TempDir()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	sequence, err := readplan.Plan(context.Background(), root, provider, readScenarioInput(scenario.Spans))
	result.PlannedNS = int64(time.Since(started))
	runtime.ReadMemStats(&after)
	result.AllocatedBytes = after.TotalAlloc - before.TotalAlloc
	if err != nil {
		tb.Fatal(err)
	}
	defer func() {
		if err := sequence.Close(); err != nil {
			tb.Error(err)
		}
	}()

	// Retain the planner's diagnostics and resident temporary files before consuming its index.
	result.Planned, result.Termination, result.PlannerReason = sequence.Order() == entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, sequence.Reason(), sequence.Reason()
	result.Diagnostics = readScenarioDiagnostics(sequence)
	if result.Termination == "" {
		result.Termination = sequence.Reason()
	}
	if observed != nil {
		result.Evaluations = observed.evaluations
		if !observed.indexed.IsZero() {
			result.IndexNS = int64(observed.indexed.Sub(started))
		}
	}
	if err := filepath.Walk(resource.TemporaryRoot(root), func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			result.TemporaryBytes += info.Size()
		}
		return nil
	}); err != nil {
		tb.Fatal(err)
	}

	// Every measured route must deliver each selected member exactly once across page boundaries.
	for {
		page, err := sequence.Next(context.Background(), 97)
		if err != nil {
			tb.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		result.Route = append(result.Route, page...)
	}
	want, got := slices.Clone(result.LogicalRoute), slices.Clone(result.Route)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		tb.Fatal("planned index changed complete member coverage")
	}
	for _, id := range result.Route {
		if file, exists := scenario.FileIDs[id]; exists {
			result.FileRoute = append(result.FileRoute, file)
		}
	}

	// Unknown model coverage must retain the ordinary complete logical read path.
	if provider == nil || scenario.IncompleteMap {
		if !slices.Equal(result.Route, result.LogicalRoute) || result.Planned {
			tb.Fatal("missing model coverage changed LOGICAL")
		}
		result.Status = "no-model"
	} else {
		// Independently re-evaluate actual delivered ranks, including the unknown-first and normal finish contracts.
		logical, err := scoreReadScenario(scenario, provider, result.LogicalRoute)
		if err != nil {
			tb.Fatal(err)
		}
		retained, err := scoreReadScenario(scenario, provider, result.Route)
		if err != nil {
			tb.Fatal(err)
		}

		// A reported best route must reproduce its complete stateful cost; missing diagnostics stay unknown.
		best := readScenarioBest(sequence)
		if best.EstimateNS <= 0 {
			best = retained
		}
		result.Baseline, result.Retained, result.Best = &logical, &retained, &best
		result.BestRouteKnown = best == retained
		if result.BestRouteKnown {
			result.BestRoute = slices.Clone(result.Route)
		}
		// Cost and admission differences are comparison results, not grounds to discard a valid route.
		result.RoutePlusPlanningNS = retained.EstimateNS + result.PlannedNS
		result.Status = "complete"
		if strings.Contains(result.Termination, "budget") {
			result.Status = "budget"
		}
		admission := scenarioAdmission(tb, scenario, provider, logical)
		result.AdmissionPolicy = admission.policy
		tb.Logf("scenario=%q logical_delta_ns=%d retained_ns=%d planning_ns=%d",
			scenario.ID, retained.EstimateNS-logical.EstimateNS, retained.EstimateNS, result.PlannedNS)

		// Beating an independently enumerated lower bound is a calculation error; missing it is a quality gap.
		if exact && len(scenario.Spans) <= 8 {
			value := exactReadScenario(tb, scenario, provider, logical)
			result.Exact = &value
			if retained.EstimateNS < value.UnconstrainedNS {
				tb.Fatal("planner cost is below independent exhaustive optimum")
			}
			tb.Logf("scenario=%q exact_gap_ns=%d", scenario.ID, retained.EstimateNS-value.UnconstrainedNS)
		}

		// Sensitivity replay remains a correctness comparison of the same frozen route and parameters.
		result.Sensitivity = readScenarioSensitivities(tb, scenario, result.Route)
		verifyReadScenarioVariants(tb, scenario, admission, result.Route, result.Sensitivity)
	}

	// A complete observation includes successful cleanup, regardless of route quality or budget expiry.
	if err := sequence.Close(); err != nil {
		tb.Fatal(err)
	}
	entries, err := os.ReadDir(resource.TemporaryRoot(root))
	if err != nil || len(entries) != 0 {
		tb.Fatalf("temporary index was not cleaned: %v entries=%d", err, len(entries))
	}
}

func readScenarioSensitivities(tb testing.TB, original readScenario, route []int64) []readScenarioSensitivity {
	// Generate no new route: perturb one frozen parameter class and replay exactly the nominal selections.
	var results []readScenarioSensitivity
	for _, factor := range []string{"locate", "longitudinal", "read"} {
		for _, multiplier := range []float64{.5, 1, 2} {
			scenario := original
			switch factor {
			case "locate":
				c := &scenario.Model.Coefficients
				c.Base *= multiplier
				c.Wrap *= multiplier
				c.Band *= multiplier
				c.LandingZone *= multiplier
				c.Direction *= multiplier
				c.StepBack *= multiplier
			case "longitudinal":
				scenario.Model.FullWrapSeconds *= multiplier
			case "read":
				scenario.Model.BytesPerSecond *= multiplier
			}
			provider := scenario.provider(tb)
			baseline, err := scoreReadScenario(scenario, provider, scenario.logical())
			if err != nil {
				tb.Fatal(err)
			}
			retained, err := scoreReadScenario(scenario, provider, route)
			if err != nil {
				tb.Fatal(err)
			}
			results = append(results, readScenarioSensitivity{factor, multiplier, baseline.EstimateNS, retained.EstimateNS})
		}
	}
	return results
}

func verifyReadScenarioVariants(tb testing.TB, s readScenario, admission readScenarioAdmission, route []int64, samples []readScenarioSensitivity) {
	tb.Helper()
	if !admission.variantsKnown {
		return
	}
	// Independent parameter perturbations must agree with the six variants advertised by production.
	for i, variant := range admission.variants {
		actual, err := scoreReadScenario(s, variant, route)
		if err != nil {
			tb.Fatal(err)
		}
		index := i/2*3 + i%2*2
		if samples[index].BaselineNS != admission.logical[i].EstimateNS || samples[index].RetainedNS != actual.EstimateNS {
			tb.Fatalf("production variant %d differs from independent frozen-parameter replay", i)
		}
	}
}

func TestReadSequenceScenarioQuality(t *testing.T) {
	profile, output := os.Getenv("YATM_READ_QUALITY_PROFILE"), os.Getenv("YATM_READ_QUALITY_OUTPUT")
	if profile == "" {
		t.Skip("explicit development, acceptance, all or regressions profile is required")
	}
	if profile != "development" && profile != "acceptance" && profile != "all" && profile != "regressions" {
		t.Fatal("invalid quality profile")
	}
	if output == "" {
		t.Fatal("quality output path is required")
	}
	var scenarios []readScenario
	if profile == "regressions" {
		scenarios = readScenarioRegressions(t)
	}
	for _, split := range []string{"development", "acceptance"} {
		if profile != "all" && profile != split {
			continue
		}
		for _, recipe := range readScenarioRecipes() {
			for _, seed := range readScenarioSeeds[split] {
				for _, backend := range []string{"tar", "ltfs"} {
					scenarios = append(scenarios, makeReadScenario(recipe, backend, split, seed))
				}
			}
		}
	}
	if profile != "regressions" {
		scenarios = append(scenarios, loadReadScenario308(t), loadReadScenarioWorkload(t, "tar"), loadReadScenarioWorkload(t, "ltfs"))
	}
	var report struct {
		Format           int                  `json:"format"`
		GeneratorVersion string               `json:"generatorVersion"`
		Profile          string               `json:"profile"`
		Complete         bool                 `json:"complete"`
		Inventory        []string             `json:"inventory"`
		Cases            []readScenarioResult `json:"cases"`
	}
	report.Format, report.GeneratorVersion, report.Profile = 1, readScenarioVersion, profile
	for _, scenario := range scenarios {
		report.Inventory = append(report.Inventory, scenario.ID)
		report.Cases = append(report.Cases, readScenarioResultInput(t, scenario))
	}
	defer func() {
		report.Complete = !t.Failed() && len(report.Cases) == len(report.Inventory)
		data, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.WriteFile(output, append(data, '\n'), 0600)
		}
		if err != nil {
			t.Error(err)
		}
	}()
	for i, scenario := range scenarios {
		t.Run(scenario.ID, func(t *testing.T) { runReadScenario(t, scenario, true, &report.Cases[i]) })
	}
}

func TestReadSequenceScenarioFrozen308(t *testing.T) {
	// Keep this fixed workload comparable for every implementation, including the frozen old baseline.
	scenario := loadReadScenario308(t)
	result := readScenarioResultInput(t, scenario)
	runReadScenario(t, scenario, false, &result)

	// Missing cost is invalid evidence; exceeding a feasible reference is a recorded quality difference.
	if result.Retained == nil {
		t.Fatal("308-member route has no complete-state cost")
	}
	t.Logf("retained_ns=%d reference_ns=%d reference_delta_ns=%d planning_ns=%d",
		result.Retained.EstimateNS, result.ReferenceNS, result.Retained.EstimateNS-result.ReferenceNS, result.PlannedNS)
}

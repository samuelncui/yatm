package media

import (
	"context"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"google.golang.org/protobuf/proto"
)

type readScenarioCost struct {
	EstimateNS    int64 `json:"estimateNS"`
	UncertaintyNS int64 `json:"uncertaintyNS"`
}

func scenarioCost(value time.Duration) readScenarioCost {
	return readScenarioCost{EstimateNS: int64(value)}
}

func (s readScenario) logical() []int64 {
	ids := make([]int64, len(s.Spans))
	for i, span := range s.Spans {
		ids[i] = span.ID
	}
	return ids
}

func scoreReadScenario(s readScenario, provider ReadOrderProvider, ids []int64) (readScenarioCost, error) {
	// This oracle traverses the public state transitions; it never calls planner deltas or scoring helpers.
	if len(ids) != len(s.Spans) {
		return readScenarioCost{}, fmt.Errorf("route covers %d of %d members", len(ids), len(s.Spans))
	}
	spans := make(map[int64]readplan.Span, len(s.Spans))
	for _, span := range s.Spans {
		spans[span.ID] = span
	}
	state, known, err := provider.Current(context.Background())
	if err != nil {
		return readScenarioCost{}, err
	}
	fixed := false
	if !known {
		capability, ok := provider, true
		if !ok || ids[0] != s.Spans[0].ID {
			return readScenarioCost{}, fmt.Errorf("unknown start requires the common complete first member")
		}
		state, known = capability.FixedFirstReadState(s.Spans[0])
		if !known {
			return readScenarioCost{}, fmt.Errorf("fixed first exit is unknown")
		}
		fixed = true
	}
	result := readScenarioCost{}
	add := func(value time.Duration) error {
		if value < 0 || int64(value) > math.MaxInt64-result.EstimateNS {
			return fmt.Errorf("invalid or overflowing complete-route cost")
		}
		result.EstimateNS += int64(value)
		return nil
	}
	for i, id := range ids {
		span, exists := spans[id]
		if !exists {
			return result, fmt.Errorf("duplicate or unknown selected member %d", id)
		}
		delete(spans, id)
		if fixed && i == 0 {
			continue
		}
		for _, interval := range span.Ranges {
			transition, known := provider.Evaluate(state, interval)
			if !known {
				return result, fmt.Errorf("member %d has unknown complete-state cost", id)
			}
			if err := add(transition.Cost); err != nil {
				return result, err
			}
			state = transition.Next
		}
	}
	if finish, ok := provider, true; ok {
		cost, known := finish.Finish(state)
		if !known {
			return result, fmt.Errorf("terminal action is unknown")
		}
		if err := add(cost); err != nil {
			return result, err
		}
	}
	return result, nil
}

func readScenarioEligible(candidate, baseline readScenarioCost) bool {
	return candidate.EstimateNS >= 0 && candidate.EstimateNS < baseline.EstimateNS
}

const readScenarioNominalPolicy = "complete-nominal-cost"

type readScenarioAdmission struct {
	policy        string
	baseline      readScenarioCost
	variants      [6]ReadOrderProvider
	logical       [6]readScenarioCost
	variantsKnown bool
}

func scenarioAdmission(tb testing.TB, s readScenario, provider ReadOrderProvider, baseline readScenarioCost) readScenarioAdmission {
	tb.Helper()
	// Sensitivity is collected independently of nominal route admission.
	result := readScenarioAdmission{policy: readScenarioNominalPolicy, baseline: baseline}
	underlying := provider
	if observed, ok := provider.(*readScenarioObservedProvider); ok {
		underlying = observed.ReadOrderProvider
	}
	capability, declared := underlying.(interface {
		ReadCostVariants() ([6]ReadOrderProvider, bool)
	})
	if !declared {
		return result
	}
	variants, known := capability.ReadCostVariants()
	if !known {
		return result
	}
	for i, variant := range variants {
		if variant == nil {
			return result
		}
		cost, err := scoreReadScenario(s, variant, s.logical())
		if err != nil {
			return result
		}
		result.logical[i] = cost
	}
	result.variants, result.variantsKnown = variants, true
	return result
}

func (a readScenarioAdmission) eligible(tb testing.TB, s readScenario, route []int64, candidate readScenarioCost) bool {
	tb.Helper()
	return readScenarioEligible(candidate, a.baseline)
}

type readScenarioExact struct {
	UnconstrainedNS    int64   `json:"unconstrainedNS"`
	EligibleNS         int64   `json:"eligibleNS"`
	UnconstrainedRoute []int64 `json:"unconstrainedRoute"`
	EligibleRoute      []int64 `json:"eligibleRoute"`
	Permutations       uint64  `json:"permutations"`
	Scope              string  `json:"scope"`
	AdmissionPolicy    string  `json:"admissionPolicy"`
	Admissibility      string  `json:"admissibility"`
}

func exactReadScenario(tb testing.TB, s readScenario, provider ReadOrderProvider, baseline readScenarioCost) readScenarioExact {
	tb.Helper()
	if len(s.Spans) > 8 {
		tb.Fatal("independent permutation oracle is limited to eight members")
	}
	admission := scenarioAdmission(tb, s, provider, baseline)
	result := readScenarioExact{UnconstrainedNS: math.MaxInt64, EligibleNS: baseline.EstimateNS,
		EligibleRoute: s.logical(), AdmissionPolicy: admission.policy, Admissibility: "complete-nominal-cost",
		Scope: "all complete file permutations under production initial-state and completion semantics; LOGICAL is always admissible; eligibleNS minimizes nominal complete-route cost, with LOGICAL always available"}
	ids := s.logical()
	start := 0
	_, known, err := provider.Current(context.Background())
	if err != nil {
		tb.Fatal(err)
	}
	if !known {
		start = 1
	}
	var visit func(int)
	visit = func(at int) {
		if at == len(ids) {
			cost, err := scoreReadScenario(s, provider, ids)
			if err != nil {
				tb.Fatal(err)
			}
			result.Permutations++
			if cost.EstimateNS < result.UnconstrainedNS {
				result.UnconstrainedNS, result.UnconstrainedRoute = cost.EstimateNS, slices.Clone(ids)
			}
			if cost.EstimateNS < result.EligibleNS && admission.eligible(tb, s, ids, cost) {
				result.EligibleNS, result.EligibleRoute = cost.EstimateNS, slices.Clone(ids)
			}
			return
		}
		for next := at; next < len(ids); next++ {
			ids[at], ids[next] = ids[next], ids[at]
			visit(at + 1)
			ids[at], ids[next] = ids[next], ids[at]
		}
	}
	visit(start)
	return result
}

func TestReadSequenceScenarioReferenceInputs(t *testing.T) {
	// Reprice the unchanged historical routes with the current LTFS evaluator, without claiming a new optimum.
	scenario := loadReadScenario308(t)
	provider := scenario.provider(t)
	logical, err := scoreReadScenario(scenario, provider, scenario.logical())
	if err != nil {
		t.Fatal(err)
	}
	reference, err := scoreReadScenario(scenario, provider, scenario.ReferenceRoute)
	if err != nil {
		t.Fatal(err)
	}
	if scenario.ReferenceNS != reference.EstimateNS {
		t.Fatalf("reference uses a different evaluator: recorded=%d actual=%+v", scenario.ReferenceNS, reference)
	}
	t.Logf("logical_ns=%d fixed_feasible_reference_ns=%d model_sha256=%s",
		logical.EstimateNS, reference.EstimateNS, readScenarioModelHash(t, scenario))
	if len(scenario.Spans) != 308 || scenario.SelectedBytes != 73_819_750_400 {
		t.Fatal("physical selection coverage changed")
	}
}

func TestReadSequenceScenarioPlanningBudget(t *testing.T) {
	// Core quality and timing inputs must retain the approved product planning window.
	for _, scenario := range []readScenario{
		loadReadScenario308(t), loadReadScenarioWorkload(t, "tar"), loadReadScenarioWorkload(t, "ltfs"),
	} {
		if scenario.PlanningBudgetNS != int64(300*time.Second) {
			t.Errorf("%s planning budget = %s, want 300s", scenario.ID, time.Duration(scenario.PlanningBudgetNS))
		}
		provider := &readScenarioObservedProvider{ReadOrderProvider: scenario.provider(t)}
		if provider.ReadPlanningBudget() != 300*time.Second {
			t.Errorf("%s provider did not retain the scenario planning budget", scenario.ID)
		}
	}
}

func TestReadSequenceScenarioGenerator(t *testing.T) {
	// The recipe inventory, independent seed sets and exact regenerated metadata are contractual fixture inputs.
	seen := make(map[uint64]bool)
	for _, split := range []string{"development", "acceptance"} {
		if len(readScenarioSeeds[split]) != 5 {
			t.Fatal("each split requires five fixed seeds")
		}
		for _, seed := range readScenarioSeeds[split] {
			if seen[seed] {
				t.Fatal("development and acceptance seeds overlap")
			}
			seen[seed] = true
			for _, recipe := range readScenarioRecipes() {
				for _, backend := range []string{"tar", "ltfs"} {
					scenario := makeReadScenario(recipe, backend, split, seed)
					if scenario.NoModel || scenario.IncompleteMap || scenario.NotApplicable != "" {
						continue
					}
					provider := scenario.provider(t)
					if _, err := scoreReadScenario(scenario, provider, scenario.logical()); err != nil {
						t.Fatalf("%s: %v", scenario.ID, err)
					}
				}
			}
		}
	}
}

func TestReadSequenceScenarioReadMethods(t *testing.T) {
	// Gap boundary cases prove opposite production choices; names alone are not coverage evidence.
	for _, backend := range []string{"tar", "ltfs"} {
		for _, name := range []string{"threshold-below", "threshold-above"} {
			for _, seed := range readScenarioSeeds["development"] {
				scenario := makeReadScenario(readScenarioRecipe{"gaps", name, 2, 0, 100}, backend, "development", seed)
				provider := scenario.provider(t)
				state, known := provider.CanonicalReadState(scenario.Spans[0])
				if !known {
					t.Fatal("first member has no complete exit")
				}
				step, known := provider.Evaluate(state, scenario.Spans[1].Ranges[0])
				want := readplan.MethodSeek
				if backend == "tar" && name == "threshold-below" {
					want = readplan.MethodContinue
				}
				if !known || step.Method != want {
					t.Fatalf("%s: gap action=%d want=%d known=%t", scenario.ID, step.Method, want, known)
				}
			}
		}
	}
}

func TestReadSequenceScenarioNaturalGroups(t *testing.T) {
	// Count real Cache/Continue bonds so the 31/32/33 cases exercise the intended boundary, not just a label.
	for _, groups := range []int{1, 10, 22, 31, 32, 33, 64} {
		for _, backend := range []string{"tar", "ltfs"} {
			for _, seed := range readScenarioSeeds["development"] {
				scenario := makeReadScenario(readScenarioRecipe{"clusters", fmt.Sprintf("groups%d", groups), groups * 4, groups, 100}, backend, "development", seed)
				provider := scenario.provider(t)
				actual := 1
				for i := 1; i < len(scenario.Spans); i++ {
					state, known := provider.CanonicalReadState(scenario.Spans[i-1])
					if !known {
						t.Fatal("unknown natural group exit")
					}
					step, known := provider.Evaluate(state, scenario.Spans[i].Ranges[0])
					if !known {
						t.Fatal("unknown natural group entry")
					}
					if step.Method != readplan.MethodCache && step.Method != readplan.MethodContinue {
						actual++
					}
				}
				if actual != groups {
					t.Fatalf("%s: %d natural groups, want %d", scenario.ID, actual, groups)
				}
			}
		}
	}
}

func TestReadSequenceScenarioUnevenGroups(t *testing.T) {
	// Real production transitions must preserve continuous groups with different member counts.
	for _, backend := range []string{"tar", "ltfs"} {
		for _, seed := range readScenarioSeeds["development"] {
			scenario := makeReadScenario(readScenarioRecipe{"difficult", "uneven-groups", 64, 0, 100}, backend, "development", seed)
			provider := scenario.provider(t)
			counts := []int{1}
			for i := 1; i < len(scenario.Spans); i++ {
				state, known := provider.CanonicalReadState(scenario.Spans[i-1])
				if !known {
					t.Fatal("unknown uneven group exit")
				}
				step, known := provider.Evaluate(state, scenario.Spans[i].Ranges[0])
				if !known {
					t.Fatal("unknown uneven group entry")
				}
				if step.Method == readplan.MethodCache || step.Method == readplan.MethodContinue {
					counts[len(counts)-1]++
				} else {
					counts = append(counts, 1)
				}
			}
			if len(counts) != 10 || slices.Min(counts) == slices.Max(counts) {
				t.Fatalf("%s: uneven continuous group sizes=%v", scenario.ID, counts)
			}
		}
	}
}

func TestReadSequenceScenarioBranchBoundaries(t *testing.T) {
	// Fixed cluster shapes must actually reach the exhaustive/indexed branches around their boundary.
	for _, backend := range []string{"tar", "ltfs"} {
		for _, groups := range []int{31, 32, 33} {
			t.Run(fmt.Sprintf("%s/%d", backend, groups), func(t *testing.T) {
				scenario := makeReadScenario(readScenarioRecipe{"clusters", fmt.Sprintf("groups%d", groups), groups * 4, groups, 100}, backend, "development", 11)
				result := readScenarioResultInput(t, scenario)
				runReadScenario(t, scenario, false, &result)
				if result.ExhaustiveRuns == nil || result.IndexedRuns == nil {
					t.Skip("frozen old planner has no reconnection branch diagnostics")
				}
				t.Logf("groups=%d exhaustive=%d indexed=%d", groups, *result.ExhaustiveRuns, *result.IndexedRuns)
				if groups <= 32 && *result.ExhaustiveRuns != int64(groups) || groups == 33 && *result.IndexedRuns != 33 {
					t.Fatalf("shape did not reach its actual reconnection branch: groups=%d exhaustive=%d indexed=%d", groups, *result.ExhaustiveRuns, *result.IndexedRuns)
				}
			})
		}
	}
}

type readScenarioGuardFixture struct {
	ReadOrderProvider
	prices map[[2]uint64]time.Duration
}

func (p *readScenarioGuardFixture) Evaluate(state readplan.State, interval readplan.Range) (readplan.Transition, bool) {
	price, known := p.prices[[2]uint64{state.Position.LogicalObject, interval.Start.LogicalObject}]
	return readplan.Transition{Cost: price, Method: readplan.MethodSeek,
		Next: readplan.State{Position: interval.End}}, known
}

func (*readScenarioGuardFixture) Finish(readplan.State) (time.Duration, bool) { return 0, true }

type readScenarioVariantFixture struct {
	ReadOrderProvider
	variants [6]ReadOrderProvider
	known    bool
}

func (p *readScenarioVariantFixture) Finish(state readplan.State) (time.Duration, bool) {
	return p.ReadOrderProvider.Finish(state)
}

func (p *readScenarioVariantFixture) ReadCostVariants() ([6]ReadOrderProvider, bool) {
	return p.variants, p.known
}

func TestReadSequenceScenarioNominalAdmission(t *testing.T) {
	// A two-member nominal improvement can lose one production variant despite passing the old interval rule.
	scenario := makeReadScenario(readScenarioRecipe{"uniform", "guard", 2, 0, 100}, "tar", "development", 11)
	scenario.Spans = nil
	for i, start := range []uint64{10, 20} {
		scenario.Spans = append(scenario.Spans, readplan.Span{ID: int64(i + 1), Ranges: []readplan.Range{{
			Start: &entity.TapeBytePosition{LogicalObject: start}, End: &entity.TapeBytePosition{LogicalObject: start + 1}, Stream: tarPrefix(1),
		}}})
	}
	base := &readScenarioGuardFixture{ReadOrderProvider: scenario.provider(t),
		prices: map[[2]uint64]time.Duration{{2, 10}: 10, {11, 20}: 10, {2, 20}: 4, {21, 10}: 4}}
	losing := &readScenarioGuardFixture{ReadOrderProvider: scenario.provider(t),
		prices: map[[2]uint64]time.Duration{{2, 10}: 10, {11, 20}: 10, {2, 20}: 12, {21, 10}: 12}}
	logical, err := scoreReadScenario(scenario, base, scenario.logical())
	if err != nil {
		t.Fatal(err)
	}
	route := []int64{2, 1}
	candidate, err := scoreReadScenario(scenario, base, route)
	if err != nil {
		t.Fatal(err)
	}

	// The legacy source has no capability; the instrumented wrapper must not invent available variants.
	legacy := scenarioAdmission(t, scenario, &readScenarioObservedProvider{ReadOrderProvider: base}, logical)
	if legacy.policy != readScenarioNominalPolicy || !legacy.eligible(t, scenario, route, candidate) {
		t.Fatal("nominal admission depends on an optional capability")
	}
	if _, known := (&readScenarioObservedProvider{ReadOrderProvider: base}).ReadCostVariants(); known {
		t.Fatal("instrumentation invented variants for the legacy provider")
	}

	// Sensitivity remains measurable but cannot change the nominal optimum or eligibility.
	variants := [6]ReadOrderProvider{base, base, base, base, base, base}
	capability := &readScenarioVariantFixture{ReadOrderProvider: base, variants: variants, known: true}
	observed := &readScenarioObservedProvider{ReadOrderProvider: capability}
	if forwarded, known := observed.ReadCostVariants(); !known || forwarded != variants {
		t.Fatal("instrumentation lost the production variants")
	}
	admission := scenarioAdmission(t, scenario, observed, logical)
	if admission.policy != readScenarioNominalPolicy || !admission.eligible(t, scenario, route, candidate) {
		t.Fatal("nominal improvement was rejected")
	}
	capability.variants[5] = losing
	admission = scenarioAdmission(t, scenario, observed, logical)
	if !admission.eligible(t, scenario, route, candidate) {
		t.Fatal("sensitivity must not veto a nominal improvement")
	}
	exact := exactReadScenario(t, scenario, observed, logical)
	if exact.UnconstrainedNS != 8 || exact.EligibleNS != 8 || exact.Admissibility != "complete-nominal-cost" {
		t.Fatalf("exact references misrepresent nominal admission: %+v", exact)
	}
	capability.known = false
	unavailable := scenarioAdmission(t, scenario, observed, logical)
	if unavailable.policy != readScenarioNominalPolicy || !unavailable.eligible(t, scenario, route, candidate) {
		t.Fatal("missing sensitivity unexpectedly blocked nominal admission")
	}
}

// readScenarioBoundMembers limits the all-pairs lower bound to selections whose complete cost matrix stays small.
const readScenarioBoundMembers = 200

// readScenarioCosts prices every directed member pair from certified exit states. Node zero is the
// prepared origin as a source and the terminal action as a target; -1 marks a pair no route can use.
// A pair whose replayed exit disagrees with its certified state leaves the matrix unknown.
func readScenarioCosts(s readScenario, provider ReadOrderProvider) ([][]int64, bool) {
	n := len(s.Spans)
	exits := make([]readplan.State, n+1)
	for i, span := range s.Spans {
		state, known := provider.CanonicalReadState(span)
		if !known {
			return nil, false
		}
		exits[i+1] = state
	}
	origin, known, err := provider.Current(context.Background())
	if err != nil {
		return nil, false
	}
	fixed := !known
	if fixed {
		// Like the route oracle, an unknown origin fixes the unpriced first LOGICAL member.
		state, known := provider.FixedFirstReadState(s.Spans[0])
		if !known {
			return nil, false
		}
		exits[1] = state
	}
	exits[0] = origin

	costs := make([][]int64, n+1)
	for i := range costs {
		costs[i] = make([]int64, n+1)
		for j := range costs[i] {
			costs[i][j] = -1
		}
	}
	for i := range costs {
		for j := range costs[i] {
			switch {
			case i == j || fixed && (i == 0) != (j == 1):
				continue
			case fixed && i == 0:
				costs[0][1] = 0
			case j == 0:
				if cost, known := provider.Finish(exits[i]); known {
					costs[i][0] = int64(cost)
				}
			default:
				state, total := exits[i], time.Duration(0)
				for _, interval := range s.Spans[j-1].Ranges {
					transition, known := provider.Evaluate(state, interval)
					if !known {
						total = -1
						break
					}
					total += transition.Cost
					state = transition.Next
				}
				if total < 0 {
					continue
				}
				if state.Stream != exits[j].Stream || !proto.Equal(state.Position, exits[j].Position) ||
					!proto.Equal(state.Cache, exits[j].Cache) {
					return nil, false
				}
				costs[i][j] = int64(total)
			}
		}
	}
	return costs, true
}

type readScenarioArc struct {
	from, to int
	cost     float64
}

// minimumArborescence returns the indices of a minimum spanning arborescence rooted at root (Chu-Liu/Edmonds).
func minimumArborescence(nodes, root int, arcs []readScenarioArc) ([]int, bool) {
	best := make([]int, nodes)
	for v := range best {
		best[v] = -1
	}
	for i, arc := range arcs {
		if arc.from != arc.to && arc.to != root && (best[arc.to] < 0 || arc.cost < arcs[best[arc.to]].cost) {
			best[arc.to] = i
		}
	}
	for v := range nodes {
		if v != root && best[v] < 0 {
			return nil, false
		}
	}

	// Follow cheapest incoming arcs; a walk that returns to its own start marks a new cycle.
	cycle, walk, cycles := make([]int, nodes), make([]int, nodes), 0
	for v := range nodes {
		cycle[v], walk[v] = -1, -1
	}
	for v := range nodes {
		x := v
		for x != root && walk[x] < 0 {
			walk[x] = v
			x = arcs[best[x]].from
		}
		if x == root || walk[x] != v {
			continue
		}
		for y := x; cycle[y] < 0; y = arcs[best[y]].from {
			cycle[y] = cycles
		}
		cycles++
	}
	if cycles == 0 {
		chosen := make([]int, 0, nodes-1)
		for v := range nodes {
			if v != root {
				chosen = append(chosen, best[v])
			}
		}
		return chosen, true
	}

	// Contract each cycle, charge entering arcs only their replacement cost, then expand the solution.
	label, next := make([]int, nodes), cycles
	for v := range nodes {
		label[v] = cycle[v]
		if label[v] < 0 {
			label[v], next = next, next+1
		}
	}
	var reduced []readScenarioArc
	var source []int
	for i, arc := range arcs {
		from, to := label[arc.from], label[arc.to]
		if from == to {
			continue
		}
		cost := arc.cost
		if cycle[arc.to] >= 0 {
			cost -= arcs[best[arc.to]].cost
		}
		reduced = append(reduced, readScenarioArc{from: from, to: to, cost: cost})
		source = append(source, i)
	}
	contracted, ok := minimumArborescence(next, label[root], reduced)
	if !ok {
		return nil, false
	}
	entered := make([]bool, nodes)
	chosen := make([]int, 0, nodes-1)
	for _, i := range contracted {
		chosen = append(chosen, source[i])
		entered[arcs[source[i]].to] = true
	}
	for v := range nodes {
		if cycle[v] >= 0 && !entered[v] {
			chosen = append(chosen, best[v])
		}
	}
	return chosen, true
}

// readScenarioLowerBound returns a Held-Karp bound below every complete route's nominal cost.
// A route is an arborescence from the origin plus one arc back to it, so each Lagrangian value of
// that relaxation with out-degree penalties is valid; subgradient steps toward upper only tighten it.
func readScenarioLowerBound(costs [][]int64, upper int64) (int64, bool) {
	nodes := len(costs)
	penalty, out := make([]float64, nodes), make([]int, nodes)
	best := math.Inf(-1)
	for iteration := range 1000 {
		var arcs []readScenarioArc
		for i := range nodes {
			for j := 1; j < nodes; j++ {
				if costs[i][j] >= 0 {
					arcs = append(arcs, readScenarioArc{from: i, to: j, cost: float64(costs[i][j]) + penalty[i]})
				}
			}
		}
		chosen, ok := minimumArborescence(nodes, 0, arcs)
		if !ok {
			return 0, false
		}
		clear(out)
		value := 0.0
		for _, i := range chosen {
			value += arcs[i].cost
			out[arcs[i].from]++
		}
		closing := -1
		for i := 1; i < nodes; i++ {
			if costs[i][0] >= 0 && (closing < 0 || float64(costs[i][0])+penalty[i] < float64(costs[closing][0])+penalty[closing]) {
				closing = i
			}
		}
		if closing < 0 {
			return 0, false
		}
		value += float64(costs[closing][0]) + penalty[closing]
		out[closing]++
		norm := 0.0
		for i := range nodes {
			value -= penalty[i]
			norm += float64((out[i] - 1) * (out[i] - 1))
		}
		best = max(best, value)
		if norm == 0 || value >= float64(upper) {
			break
		}
		step := 2 * math.Pow(0.995, float64(iteration)) * (float64(upper) - value) / norm
		for i := range nodes {
			penalty[i] += step * float64(out[i]-1)
		}
	}
	return int64(math.Floor(best)), true
}

func TestReadSequenceScenarioLowerBound(t *testing.T) {
	// Every exact small case checks that the bound never exceeds the true optimum of the same costs.
	for _, seed := range readScenarioSeeds["development"] {
		for _, backend := range []string{"tar", "ltfs"} {
			scenario := makeReadScenario(readScenarioRecipe{"difficult", "exact-small", 7, 0, 100}, backend, "development", seed)
			provider := scenario.provider(t)
			logical, err := scoreReadScenario(scenario, provider, scenario.logical())
			if err != nil {
				t.Fatal(err)
			}
			exact := exactReadScenario(t, scenario, provider, logical)
			costs, known := readScenarioCosts(scenario, provider)
			if !known {
				t.Fatalf("%s: cost matrix is unknown", scenario.ID)
			}
			bound, known := readScenarioLowerBound(costs, exact.UnconstrainedNS)
			if !known || bound < 0 || bound > exact.UnconstrainedNS {
				t.Fatalf("%s: bound=%d known=%t optimum=%d", scenario.ID, bound, known, exact.UnconstrainedNS)
			}
		}
	}
}

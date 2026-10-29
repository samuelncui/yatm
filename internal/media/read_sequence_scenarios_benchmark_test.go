package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/samuelncui/yatm/internal/resource"
)

func BenchmarkReadSequenceScenarios(b *testing.B) {
	// Time one fixed representative subset; exhaustive seed quality remains a separate untimed test profile.
	for _, backend := range []string{"tar", "ltfs"} {
		for _, groups := range []int{31, 32, 33} {
			name := fmt.Sprintf("%s-clusters%d", backend, groups)
			b.Run(name, func(b *testing.B) {
				scenario := makeReadScenario(readScenarioRecipe{"clusters", fmt.Sprintf("groups%d", groups), groups * 4, groups, 100}, backend, "development", 11)
				benchmarkReadScenario(b, scenario)
			})
		}
		b.Run(backend+"-uniform20", func(b *testing.B) {
			benchmarkReadScenario(b, makeReadScenario(readScenarioRecipe{"uniform", "select20", 400, 0, 20}, backend, "development", 11))
		})
		for _, recipe := range []readScenarioRecipe{
			{"sizes", "equal-select20", 120, 0, 20},
			{"sizes", "small-select20", 120, 0, 20},
			{"sizes", "bimodal-select20", 120, 0, 20},
			{"sizes", "long-tail-select20", 120, 0, 20},
			{"gaps", "alternating", 24, 0, 100},
			{"layout", "partial-tail", 24, 0, 100},
			{"files", "multi-extent", 24, 0, 100},
			{"files", "shared-record", 24, 0, 100},
			{"files", "append", 24, 0, 100},
			{"cost", "reading", 24, 0, 100},
			{"difficult", "uneven-groups", 64, 0, 100},
			{"difficult", "uncertain", 24, 0, 100},
		} {
			scenario := makeReadScenario(recipe, backend, "development", 11)
			if scenario.NotApplicable != "" {
				continue
			}
			b.Run(backend+"-"+recipe.Family+"-"+recipe.Name, func(b *testing.B) { benchmarkReadScenario(b, scenario) })
		}

		b.Run(backend+"-1p5TB", func(b *testing.B) { benchmarkReadScenario(b, loadReadScenarioWorkload(b, backend)) })
	}
	b.Run("ltfs-308", func(b *testing.B) { benchmarkReadScenario(b, loadReadScenario308(b)) })
}

func benchmarkReadScenario(b *testing.B, scenario readScenario) {
	b.Helper()
	root := b.TempDir()
	var planning, indexing, fullScoring time.Duration
	var evaluations, totalEvaluations uint64
	totalEvaluationsKnown := true
	var logical, retained readScenarioCost
	var temporary int64
	b.ReportAllocs()
	b.ResetTimer()
	b.StopTimer()
	for range b.N {
		// Provider/fixture construction and independent route validation are outside the timed operation.
		provider := &readScenarioObservedProvider{ReadOrderProvider: scenario.provider(b)}
		b.StartTimer()
		started := time.Now()
		sequence, err := readplan.Plan(context.Background(), root, provider, readScenarioInput(scenario.Spans))
		elapsed := time.Since(started)
		if err != nil {
			b.Fatal(err)
		}
		var route []int64
		for {
			page, err := sequence.Next(context.Background(), 256)
			if err != nil {
				b.Fatal(err)
			}
			if len(page) == 0 {
				break
			}
			route = append(route, page...)
		}
		// Retain file allocation evidence before Close removes the disposable index.
		size := int64(0)
		if err := filepath.Walk(resource.TemporaryRoot(root), func(_ string, info os.FileInfo, err error) error {
			if err == nil && info.Mode().IsRegular() {
				size += info.Size()
			}
			return err
		}); err != nil {
			b.Fatal(err)
		}
		temporary = max(temporary, size)
		if err := sequence.Close(); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		// Keep search and termination evidence outside timed planning, consumption and cleanup.
		searchDiagnostics := readScenarioDiagnostics(sequence)
		diagnostics, err := json.Marshal(searchDiagnostics)
		if err != nil {
			b.Fatal(err)
		}
		b.Logf("scenario=%q termination=%q planner_reason=%q diagnostics=%s",
			scenario.ID, sequence.Reason(), sequence.Reason(), diagnostics)
		planning += elapsed
		if !provider.indexed.IsZero() {
			indexing += provider.indexed.Sub(started)
		}
		evaluations += provider.evaluations
		if searchDiagnostics.TotalEvaluations == nil {
			totalEvaluationsKnown = false
		} else {
			totalEvaluations += *searchDiagnostics.TotalEvaluations
		}
		want, got := scenario.logical(), slices.Clone(route)
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(want, got) {
			b.Fatal("benchmark index lost complete member coverage")
		}
		logical, err = scoreReadScenario(scenario, provider, scenario.logical())
		if err != nil {
			b.Fatal(err)
		}
		// Measure one independent complete retained-route replay, outside the planner/consumption timer.
		scoreStarted := time.Now()
		retained, err = scoreReadScenario(scenario, provider, route)
		fullScoring += time.Since(scoreStarted)
		if err != nil {
			b.Fatal(err)
		}
		entries, err := os.ReadDir(resource.TemporaryRoot(root))
		if err != nil || len(entries) != 0 {
			b.Fatalf("benchmark cleanup failed: %v count=%d", err, len(entries))
		}
	}
	b.ReportMetric(float64(planning)/float64(b.N), "plan-ns/op")
	b.ReportMetric(float64(indexing)/float64(b.N), "index-ns/op")
	b.ReportMetric(float64(planning-indexing)/float64(b.N), "search-score-ns/op")
	b.ReportMetric(float64(fullScoring)/float64(b.N), "full-score-ns/op")
	b.ReportMetric(float64(evaluations)/float64(b.N), "evaluations/op")
	if totalEvaluationsKnown {
		b.ReportMetric(float64(totalEvaluations)/float64(b.N), "total-evaluations/op")
	}
	b.ReportMetric(float64(temporary), "temporary-B")
	b.ReportMetric(float64(logical.EstimateNS)/1e9, "logical-model-s/op")
	b.ReportMetric(float64(retained.EstimateNS)/1e9, "retained-model-s/op")
	b.ReportMetric((float64(retained.EstimateNS)+float64(planning)/float64(b.N))/1e9, "model-plus-plan-s/op")
	// The bound needs all member pairs, so large selections omit it rather than report a weaker value.
	if len(scenario.Spans) <= readScenarioBoundMembers {
		costs, known := readScenarioCosts(scenario, scenario.provider(b))
		if known {
			if bound, known := readScenarioLowerBound(costs, retained.EstimateNS); known {
				b.ReportMetric(float64(bound)/1e9, "lower-bound-model-s/op")
			}
		}
	}
	reportReadSequenceRSS(b)
}

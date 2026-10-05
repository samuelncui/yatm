package scan

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func contentSpec(policy entity.ScanResultPolicy) *entity.ScanJobSpec {
	return &entity.ScanJobSpec{Selections: scanLocationSelections(1), SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ, ResultPolicy: policy}
}

// contentEntries writes real files and their manifest rows for one Location.
func contentEntries(t *testing.T, r *runner, locationID int64, contents ...string) []*Entry {
	t.Helper()
	root := t.TempDir()
	entries := make([]*Entry, 0, len(contents))
	for index, content := range contents {
		name := fmt.Sprintf("file-%02d", index)
		filename := filepath.Join(root, name)
		require.NoError(t, os.WriteFile(filename, []byte(content), 0o644))
		info, err := os.Stat(filename)
		require.NoError(t, err)
		entry := &Entry{LocationID: locationID, Path: name, SourcePath: filename,
			Size: info.Size(), Mode: uint32(info.Mode()), MtimeNS: info.ModTime().UnixNano(), NeedsHash: true}
		require.NoError(t, r.db.Create(entry).Error)
		entries = append(entries, entry)
	}
	return entries
}

// completedResult reports the content facts a complete read observes for one entry. The item is
// the entry's own ACP item, which is what the results callback asserts Result.Job back to.
func completedResult(t *testing.T, entry *Entry) acp.Result {
	t.Helper()
	content, err := os.ReadFile(entry.SourcePath)
	require.NoError(t, err)
	info, err := os.Stat(entry.SourcePath)
	require.NoError(t, err)
	hash := sha256.Sum256(content)
	return acp.Result{Job: &contentItem{Entry: entry}, Size: info.Size(), Mode: info.Mode(),
		ModTime: info.ModTime(), SHA256: hash[:]}
}

// flushLimits is one content-phase configuration: the manifest page, the shared writer's queue
// capacity and batch size, and the ACP flush interval.
func flushLimits(readBatch, writeBufferMax, intervalMs int) *Config {
	return frozenLimits(&Config{Spec: contentSpec(entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY)})
}

func flushSettings(readBatch, writeBufferMax, intervalMS int) *entity.JobExecutionSettings {
	return &entity.JobExecutionSettings{
		ReadBatch: int32(readBatch), ReadBufferMax: 16, WriteBufferMax: int32(writeBufferMax),
		WriteBatchSize: int32(writeBufferMax), FlushIntervalMs: int32(intervalMS),
	}
}

// statementRecorder counts the manifest statements one test issues.
type statementRecorder struct {
	lock    sync.Mutex
	queries []string
	creates []string
}

func (s *statementRecorder) record(target *[]string, sql string) {
	s.lock.Lock()
	defer s.lock.Unlock()
	*target = append(*target, sql)
}

// createCount and create read the recorded statements from outside the flush goroutine.
func (s *statementRecorder) createCount() int {
	s.lock.Lock()
	defer s.lock.Unlock()
	return len(s.creates)
}

func (s *statementRecorder) create(index int) string {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.creates[index]
}

func (s *statementRecorder) queryCount() int {
	s.lock.Lock()
	defer s.lock.Unlock()
	return len(s.queries)
}

func (s *statementRecorder) lookupCount() int {
	s.lock.Lock()
	defer s.lock.Unlock()
	count := 0
	for _, sql := range s.queries {
		// A per-item result lookup reads exactly one manifest row by its primary key.
		if strings.Contains(sql, "`id` = ?") || strings.Contains(sql, " id = ?") {
			count++
		}
	}
	return count
}

func recordStatements(t *testing.T, db *gorm.DB) *statementRecorder {
	t.Helper()
	recorder := new(statementRecorder)
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:record-query", func(tx *gorm.DB) {
		if tx.Statement.Table == "entries" {
			recorder.record(&recorder.queries, tx.Statement.SQL.String())
		}
	}))
	require.NoError(t, db.Callback().Create().After("gorm:create").Register("test:record-create", func(tx *gorm.DB) {
		if tx.Statement.Table == "entries" {
			recorder.record(&recorder.creates, tx.Statement.SQL.String())
		}
	}))
	return recorder
}

func TestContentHashPolicyFollowsPhaseAndResultPolicy(t *testing.T) {
	// One ACP policy value expresses each combination of Scan signature and result policy.
	location := &entity.ScanJobSpec{Selections: scanLocationSelections(1), SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FILL_MISSING,
		ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS}
	media := &entity.ScanJobSpec{MediaId: 1, SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FILL_MISSING,
		ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_INVENTORY}
	verify := &entity.ScanJobSpec{MediaId: 1, SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ,
		ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES}
	forced := &entity.ScanJobSpec{MediaId: 1, SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ,
		ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_INVENTORY}
	for _, tc := range []struct {
		name    string
		spec    *entity.ScanJobSpec
		session mediapkg.ReadSession
		want    acp.HashPolicy
	}{
		{"Location observes its own content and refreshes the cache", location, nil, acp.HashReadRefresh},
		{"Location verification reads real content", verify, nil, acp.HashRead},
		{"Media fill-missing reuses the cache", media, &sequentialCheckSession{checkSession{}}, acp.HashCachedOrReadRefresh},
		{"Media verification never reuses a stored hash", verify, &sequentialCheckSession{checkSession{}}, acp.HashRead},
		{"Forced Media read cannot reuse a stored hash", forced, &sequentialCheckSession{checkSession{}}, acp.HashReadRefresh},
		{"Known-only is expressed as cache-only", &entity.ScanJobSpec{Selections: scanLocationSelections(1), SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY},
			nil, acp.HashCachedOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, contentHashPolicy(frozenLimits(&Config{Spec: tc.spec}), tc.session))
		})
	}
}

func TestContentKnownOnlyNeverReadsContent(t *testing.T) {
	// A cache-only miss stays unknown: the phase returns before ACP could hash it.
	r := newProgressRunner(t)
	entry := contentEntries(t, r, 1, "content")[0]
	config := frozenLimits(&Config{Spec: &entity.ScanJobSpec{Selections: scanLocationSelections(1), SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
		ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY}})
	require.NoError(t, r.readContent(testAttemptContext(context.Background()), config, &Scope{LocationID: 1}, nil, nil))

	var stored Entry
	require.NoError(t, r.db.First(&stored, entry.ID).Error)
	require.True(t, stored.NeedsHash)
	require.Empty(t, stored.SHA256)
	require.Equal(t, entity.ScanFinding_SCAN_FINDING_NOT_CHECKED, stored.Finding)
}

func TestContentReadsTheStoredSourcePath(t *testing.T) {
	// Indexing resolves the source path once; the read stage must not ask the session again.
	r := newProgressRunner(t)
	entry := contentEntries(t, r, 0, "stored content")[0]
	session := &countingSourceSession{checkSession: checkSession{root: t.TempDir()}}
	config := frozenLimits(&Config{Spec: &entity.ScanJobSpec{MediaId: 1, SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ,
		ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_INVENTORY}})
	require.NoError(t, r.readContent(testAttemptContext(context.Background()), config, &Scope{}, nil, session))
	require.Zero(t, session.resolutions, "the stored path is authoritative")

	var stored Entry
	require.NoError(t, r.db.First(&stored, entry.ID).Error)
	require.False(t, stored.NeedsHash)
	expected := sha256.Sum256([]byte("stored content"))
	require.Equal(t, expected[:], stored.SHA256)
}

type countingSourceSession struct {
	checkSession
	resolutions int
}

func (s *countingSourceSession) SourcePath(name string) (string, error) {
	s.resolutions++
	return s.checkSession.SourcePath(name)
}

func TestContentFlushPersistsBatchedStatements(t *testing.T) {
	// Five results reach the database in batches of write_batch_size instead of one statement per
	// result, and the item already carries its row, so no per-item result lookup happens.
	r := newProgressRunner(t)
	entries := contentEntries(t, r, 1, "a", "bb", "ccc", "dddd", "eeeee")
	recorder := recordStatements(t, r.db)
	config := flushLimits(8, 2, 60000)
	ctx := executor.WithJobExecutionSettings(context.Background(), flushSettings(8, 2, 60000))
	require.NoError(t, r.readContent(ctx, config, &Scope{LocationID: 1}, nil, nil))

	// The shared writer persists one statement per batch, and one batch carries at most two rows.
	require.GreaterOrEqual(t, recorder.createCount(), (len(entries)+1)/2, "results are batched")
	require.LessOrEqual(t, recorder.createCount(), len(entries))
	require.LessOrEqual(t, strings.Count(recorder.create(0), "),(")+1, 2, "one statement carries one batch")
	require.NotZero(t, recorder.queryCount(), "the manifest is paged")
	require.Zero(t, recorder.lookupCount(), "a content result is never looked up by id")

	// The same matcher does detect the per-item lookup the phase no longer performs.
	var lookedUp Entry
	require.NoError(t, r.db.First(&lookedUp, entries[0].ID).Error)
	require.Equal(t, 1, recorder.lookupCount())

	var stored []*Entry
	require.NoError(t, r.db.Order("id").Find(&stored).Error)
	require.Len(t, stored, len(entries))
	for _, row := range stored {
		require.False(t, row.NeedsHash)
		require.Len(t, row.SHA256, sha256.Size)
	}
}

// openContentRun starts one real ACP run over a content stream, so a test observes the engine's
// own result buffering and flush cadence instead of simulating them.
func openContentRun(ctx context.Context, t *testing.T, r *runner, stream *contentStream) *acp.StreamCopyer {
	t.Helper()
	settings := settingspkg.DefaultJobExecution()
	options := []acp.Option{
		acp.WithHashPolicy(acp.HashRead),
		acp.WithReadBuffer(int(settings.GetReadBufferMax())),
		acp.WithLogger(r.logger),
		acp.WithEventHandler(stream.eventHandler()),
	}
	engine, err := acp.NewStream(ctx, stream.onResults, append(options, resultOptions(settings)...)...)
	require.NoError(t, err)
	return engine
}

func TestContentResultsFollowTheACPFlushInterval(t *testing.T) {
	// A single success result below the result buffer limit is still persisted by the flush
	// interval while the run is open, which is what bounds the loss of an unclean stop.
	r := newProgressRunner(t)
	entry := contentEntries(t, r, 1, "aa")[0]
	recorder := recordStatements(t, r.db)
	stream, err := openTestContentStream(
		t, context.Background(), r, flushLimits(64, 64, 100), &Scope{LocationID: 1}, nil,
		flushSettings(64, 64, 100),
	)
	require.NoError(t, err)
	engine := openContentRun(context.Background(), t, r, stream)
	require.NoError(t, engine.Submit(&contentItem{Entry: entry}))

	// The run is still open: only the interval can deliver this result.
	require.Eventually(t, func() bool { return recorder.createCount() == 1 }, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, engine.Close())
	require.NoError(t, engine.Wait())
	require.NoError(t, stream.writer.Close())
	require.Equal(t, 1, recorder.createCount(), "the closed run flushed nothing twice")
}

func TestContentCallbackOnlyEnqueuesAcceptedResults(t *testing.T) {
	// The callback hands the batch it received to the writer and returns: it never waits for the
	// statement, and the drain still persists everything the callback accepted.
	r := newProgressRunner(t)
	entries := contentEntries(t, r, 1, "a", "bb", "ccc")
	recorder := recordStatements(t, r.db)
	stream, err := openTestContentStream(
		t, context.Background(), r, flushLimits(8, 8, 60000), &Scope{LocationID: 1}, nil,
		flushSettings(8, 8, 60000),
	)
	require.NoError(t, err)

	// Hold the one statement the writer wants to issue, so the callback's own I/O is observable.
	released := make(chan struct{})
	started := make(chan struct{}, 1)
	require.NoError(t, r.db.Callback().Create().Before("gorm:create").Register("test:block-entry-write", func(tx *gorm.DB) {
		if tx.Statement.Table != "entries" {
			return
		}
		started <- struct{}{}
		<-released
	}))

	results := make([]acp.Result, 0, len(entries))
	for _, entry := range entries {
		results = append(results, completedResult(t, entry))
	}
	require.NoError(t, stream.onResults(results))
	<-started
	require.Zero(t, recorder.createCount(), "the callback returned while its batch was still unwritten")
	close(released)

	require.NoError(t, stream.writer.Close(), "the drain persists everything the callback accepted")
	require.LessOrEqual(t, recorder.createCount(), len(entries), "no batch exceeds the results one callback accepted")
	var stored []*Entry
	require.NoError(t, r.db.Order("id").Find(&stored).Error)
	require.Len(t, stored, len(entries))
	for _, row := range stored {
		require.False(t, row.NeedsHash)
	}
}

func TestContentFailureStopsScopeUnlessVerifying(t *testing.T) {
	// A source missing at its stored path is reported and left for a later Scan: ordinary
	// policies stop their scope, verification records an independent per-file finding.
	for _, tc := range []struct {
		name    string
		policy  entity.ScanResultPolicy
		stops   bool
		finding entity.ScanFinding
	}{
		{"ordinary content failure stops the scope", entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY, true, entity.ScanFinding_SCAN_FINDING_NOT_CHECKED},
		{"verification records the finding and continues", entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES, false, entity.ScanFinding_SCAN_FINDING_MISSING},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newProgressRunner(t)
			entry := &Entry{LocationID: 1, Path: "lost", SourcePath: filepath.Join(t.TempDir(), "lost"), Size: 3, NeedsHash: true}
			require.NoError(t, r.db.Create(entry).Error)
			config := frozenLimits(&Config{Spec: &entity.ScanJobSpec{MediaId: 1, SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ,
				ResultPolicy: tc.policy}})
			err := r.readContent(testAttemptContext(context.Background()), config, &Scope{LocationID: 1}, nil, nil)

			var stored Entry
			require.NoError(t, r.db.First(&stored, entry.ID).Error)
			if tc.stops {
				require.ErrorIs(t, err, os.ErrNotExist)
				require.True(t, stored.NeedsHash)
			} else {
				require.NoError(t, err)
				require.False(t, stored.NeedsHash)
				require.NotZero(t, stored.CheckedAtNS)
			}
			require.Equal(t, tc.finding, stored.Finding)
		})
	}
}

func TestContentItemFailureFollowsResultPolicy(t *testing.T) {
	// A failure the results callback receives is handed to the writer with everything else.
	// Verification records the finding and continues; every other policy reports the cause, which
	// stops the feed exactly like a caller cancellation.
	cause := errors.New("read failed")
	for _, tc := range []struct {
		name    string
		policy  entity.ScanResultPolicy
		wantErr error
		finding entity.ScanFinding
	}{
		{"ordinary item failure stops the scope", entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY, cause, entity.ScanFinding_SCAN_FINDING_NOT_CHECKED},
		{"verification records the item failure", entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES, nil, entity.ScanFinding_SCAN_FINDING_UNREADABLE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newProgressRunner(t)
			entry := contentEntries(t, r, 1, "content")[0]
			config := flushLimits(8, 8, 1000)
			config.Spec.ResultPolicy = tc.policy
			stream, err := openTestContentStream(t, context.Background(), r, config, &Scope{LocationID: 1}, nil)
			require.NoError(t, err)

			// The writer may record the failure before the callback returns; Close reports it either way.
			callbackErr := stream.onResults([]acp.Result{{Job: &contentItem{Entry: entry}, Err: cause}})
			writeErr := stream.writer.Close()
			if tc.wantErr == nil {
				require.NoError(t, callbackErr)
				require.NoError(t, writeErr)
			} else {
				if callbackErr != nil {
					require.ErrorIs(t, callbackErr, tc.wantErr)
				}
				require.ErrorIs(t, writeErr, tc.wantErr)
				require.ErrorIs(t, stream.onResults([]acp.Result{{Job: &contentItem{Entry: entry}, Err: cause}}), tc.wantErr,
					"a recorded failure stops the feed")
			}

			// Verification persists the finding before the drain returns.
			var stored Entry
			require.NoError(t, r.db.First(&stored, entry.ID).Error)
			require.Equal(t, tc.finding, stored.Finding)
		})
	}
}

func TestContentAbandonedItemIsAccountedWithoutAFinding(t *testing.T) {
	// An item a graceful stop abandons arrives through the failure callback: it is counted and
	// never mistaken for a per-file finding, and it produces no result to persist.
	r := newProgressRunner(t)
	entry := contentEntries(t, r, 1, "content")[0]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream, err := openTestContentStream(
		t, ctx, r, flushLimits(8, 8, 1000), &Scope{LocationID: 1}, nil, flushSettings(8, 8, 1000),
	)
	require.NoError(t, err)
	_, persists := stream.failed(entry, context.Canceled)
	require.False(t, persists, "an abandoned item has no result to persist")
	_, processed := stream.counters()
	require.EqualValues(t, 1, processed)
	require.NoError(t, stream.writer.Close())

	var stored Entry
	require.NoError(t, r.db.First(&stored, entry.ID).Error)
	require.True(t, stored.NeedsHash)
	require.Equal(t, entity.ScanFinding_SCAN_FINDING_NOT_CHECKED, stored.Finding)
}

func TestContentCancellationAccountsForEveryAcceptedItem(t *testing.T) {
	// A graceful stop still reports every accepted item through the results callback, and the
	// results already produced keep a complete record.
	r := newProgressRunner(t)
	entries := contentEntries(t, r, 1, "a", "bb", "ccc", "dddd", "eeeee", "ffffff")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Stop the feed as soon as the first content results are ready to be written, which is
	// after ACP accepted them and while later items are still in flight.
	var once sync.Once
	require.NoError(t, r.db.Callback().Create().Before("gorm:create").Register("test:cancel-on-first-flush", func(tx *gorm.DB) {
		rows, ok := tx.Statement.Dest.(*[]*Entry)
		if !ok || tx.Statement.Table != "entries" {
			return
		}
		for _, row := range *rows {
			if !row.NeedsHash {
				once.Do(cancel)
				return
			}
		}
	}))

	config := frozenLimits(&Config{Spec: &entity.ScanJobSpec{MediaId: 1, SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ,
		ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES}})
	stream, err := openTestContentStream(t, ctx, r, config, &Scope{LocationID: 1}, nil)
	require.NoError(t, err)

	// The engine reports how many items it accepted, which is the set that must be accounted for.
	var accepted atomic.Int64
	handler := stream.eventHandler()
	settings := flushSettings(2, 16, 100)
	options := []acp.Option{
		acp.WithHashPolicy(acp.HashRead),
		acp.WithReadBuffer(int(settings.GetReadBufferMax())),
		acp.WithLogger(r.logger),
		acp.WithEventHandler(func(event acp.Event) {
			if value, ok := event.(*acp.EventUpdateCount); ok {
				accepted.Store(value.Files)
			}
			handler(event)
		}),
	}
	engine, err := acp.NewStream(ctx, stream.onResults, append(options, resultOptions(settings)...)...)
	require.NoError(t, err)
	runErr := stream.consume(ctx, engine)
	require.NoError(t, stream.writer.Close(), "the drain still persists the results the stopped run accepted")
	require.True(t, runErr == nil || errors.Is(runErr, context.Canceled), "a graceful stop is not a pipeline failure: %v", runErr)

	// Every accepted item reached exactly one terminal outcome, and never more than once.
	_, processed := stream.counters()
	require.NotZero(t, accepted.Load())
	require.EqualValues(t, accepted.Load(), processed)

	// Persisted results are complete: a stopped run leaves no half-recorded finding.
	var stored []*Entry
	require.NoError(t, r.db.Order("id").Find(&stored).Error)
	require.Len(t, stored, len(entries))
	persisted := 0
	for _, row := range stored {
		if row.NeedsHash {
			continue
		}
		persisted++
		require.NotZero(t, row.CheckedAtNS)
		require.Len(t, row.ActualHash, sha256.Size)
	}
	require.NotZero(t, persisted, "the closing flush still persists the results it holds")
	require.LessOrEqual(t, persisted, int(accepted.Load()))
}

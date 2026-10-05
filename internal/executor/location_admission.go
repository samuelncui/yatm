package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
)

const admissionPageSize = 128

// AdmitLocationEntries resolves all higher-priority matches before creating unmatched Files.
// Only the supplied live batch and positively absent old paths participate; no global scan occurs.
func (e *Executor) AdmitLocationEntries(ctx context.Context, refs []*entity.LocationEntryRef) ([]*library.FileLocation, error) {
	if len(refs) == 0 || len(refs) > 1000 || refs[0] == nil {
		return nil, fmt.Errorf("admit between 1 and 1000 live files")
	}
	// Gathering evidence is bounded and performs no full-file reads. Cache misses remain optional.
	ordered := append([]*entity.LocationEntryRef(nil), refs...)
	for _, ref := range ordered {
		if ref == nil || ref.LocationId != refs[0].LocationId {
			return nil, library.ErrLocationConflict
		}
		if IsLocationTrashPath(ref.Path) {
			return nil, fmt.Errorf("Trash content cannot be added to Library")
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	var location *library.Location
	matches := make([]*library.ObservationAdmission, 0, len(ordered))
	positions := make([]*library.ObservedEntry, 0, len(ordered))
	claimed := map[int64]bool{}
	for index, ref := range ordered {
		if index > 0 && ordered[index-1].Path == ref.Path {
			return nil, fmt.Errorf("duplicate admission path %q", ref.Path)
		}
		current, name, info, err := e.ResolveLocationEntry(ctx, ref)
		if err != nil {
			return nil, err
		}
		location = current
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("only ordinary files have Library associations")
		}
		facts, err := InspectLocationFacts(info)
		if err != nil {
			return nil, err
		}
		keys := ObserveTracking(location, info)
		p := &library.ObservedEntry{Path: ref.Path, Size: facts.SizeBytes, Mode: facts.Mode, MtimeNS: facts.MtimeNs, TrackingKeys: keys}
		cached, valid, _ := acp.ReadCachedSignature(name)
		if valid && cached.Size == p.Size && cached.MtimeNS == p.MtimeNS {
			p.Hash = append([]byte(nil), cached.SHA256[:]...)
			p.Signature, err = library.NewFileSignature(p.Hash, p.Size)
			if err != nil {
				return nil, err
			}
		}
		previous, err := e.lib.GetFileLocationAtPath(ctx, location.ID, p.Path)
		if err != nil {
			return nil, err
		}
		if previous != nil {
			p.FileID, claimed[previous.FileID] = previous.FileID, true
			preserveObservedSignature(p, previous)
		}
		matches = append(matches, &library.ObservationAdmission{Observation: p, Previous: previous})
		positions = append(positions, p)
	}

	// Every path match precedes signature and native rounds, including later rows in the page.
	for _, rule := range []library.TrackingKind{"signature", library.TrackingNative} {
		if err := e.matchAdmissionRound(ctx, location, rule, positions, matches, claimed); err != nil {
			return nil, err
		}
	}
	for _, match := range matches {
		if match.Previous != nil && match.Previous.Path != match.Observation.Path {
			if !e.absentAdmissionPath(location, match.Previous) {
				return nil, library.ErrLocationConflict
			}
		}
	}
	return e.lib.AdmitObservations(ctx, location.ID, matches)
}

func (e *Executor) matchAdmissionRound(ctx context.Context, location *library.Location, kind library.TrackingKind, positions []*library.ObservedEntry, matches []*library.ObservationAdmission, claimed map[int64]bool) error {
	// Candidate queries use existing signature/tracking indexes and page by old File identity.
	var signatures [][]byte
	for _, p := range positions {
		if p.FileID == 0 && len(p.Signature) != 0 {
			signatures = append(signatures, p.Signature)
		}
	}
	var after int64
	for {
		var candidates []*library.FileTrackingKey
		originals := map[int64]*library.FileLocation{}
		if kind == "signature" {
			rows, err := e.lib.AdmissionSignatureCandidates(ctx, location, signatures, after, admissionPageSize)
			if err != nil {
				return err
			}
			for _, row := range rows {
				originals[row.FileID] = row
				candidates = append(candidates, &library.FileTrackingKey{FileID: row.FileID, KeyValue: row.Signature})
			}
		} else {
			var err error
			candidates, err = e.lib.AdmissionCandidates(ctx, location, kind, positions, after, admissionPageSize)
			if err != nil {
				return err
			}
		}
		if len(candidates) == 0 {
			return nil
		}
		for _, candidate := range candidates {
			after = candidate.FileID
			if claimed[candidate.FileID] {
				continue
			}
			old, found := originals[candidate.FileID]
			if !found {
				var err error
				old, err = e.lib.GetFileLocation(ctx, candidate.FileID)
				if err != nil {
					return err
				}
			}
			if old != nil && !e.absentAdmissionPath(location, old) {
				continue
			}
			for _, match := range matches {
				p := match.Observation
				if p.FileID != 0 || !admissionEvidenceMatches(kind, candidate, p) {
					continue
				}
				p.FileID, match.Previous, claimed[candidate.FileID] = candidate.FileID, old, true
				if old != nil {
					if kind == library.TrackingNative && old.LocationID == location.ID && old.Size == p.Size && old.Mode == p.Mode && old.MtimeNS == p.MtimeNS && len(p.Hash) == 0 {
						p.Hash, p.Signature = old.Hash, old.Signature
					}
					preserveObservedSignature(p, old)
				}
				break
			}
		}
	}
}

func admissionEvidenceMatches(kind library.TrackingKind, candidate *library.FileTrackingKey, observation *library.ObservedEntry) bool {
	if kind == "signature" {
		return len(observation.Signature) != 0 && bytes.Equal(candidate.KeyValue, observation.Signature)
	}
	for _, key := range observation.TrackingKeys {
		if key.Kind != kind || key.Scope != candidate.Scope || !bytes.Equal(key.KeyValue, candidate.KeyValue) {
			continue
		}
		if kind == library.TrackingNative && candidate.Details.BirthNS != 0 && key.Details.BirthNS != candidate.Details.BirthNS {
			continue
		}
		if kind == library.TrackingNative && candidate.Details.Generation != 0 && key.Details.Generation != candidate.Details.Generation {
			continue
		}
		return true
	}
	return false
}

func (e *Executor) absentAdmissionPath(location *library.Location, old *library.FileLocation) bool {
	// An inaccessible root or permissions failure is not evidence of a move.
	if old.LocationID != location.ID {
		return false
	}
	if _, _, err := e.CheckLocationPath(location, old.Path); !os.IsNotExist(err) {
		return false
	}
	_, _, err := e.CheckLocationPath(location, "")
	return err == nil
}

func preserveObservedSignature(observation *library.ObservedEntry, previous *library.FileLocation) {
	// A valid cache may reproduce content hashes, but must not recode a known opaque signature.
	if len(observation.Hash) != 0 && len(previous.Signature) != 0 && observation.Size == previous.Size && bytes.Equal(observation.Hash, previous.Hash) {
		observation.Signature = previous.Signature
	}
}

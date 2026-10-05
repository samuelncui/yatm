package observation

import (
	"bytes"
	"context"

	"github.com/sirupsen/logrus"
)

type matcher struct {
	name     string
	criteria func(*Original) (MatchCriteria, bool)
}

// Each complete global round precedes the next rule; later evidence never overrides a match.
var relocationMatchers = []matcher{
	{"path", func(old *Original) (MatchCriteria, bool) {
		if old.Path == "" {
			return MatchCriteria{}, false
		}
		return MatchCriteria{Kind: MatchPath, Path: old.Path}, true
	}},
	{"signature", func(old *Original) (MatchCriteria, bool) {
		if len(old.Signature) == 0 {
			return MatchCriteria{}, false
		}
		return MatchCriteria{Kind: MatchSignature, Signature: old.Signature}, true
	}},
	{"native identity", func(old *Original) (MatchCriteria, bool) {
		key := old.Evidence
		if len(key.NativeKey) == 0 || key.NativeScope == "" {
			return MatchCriteria{}, false
		}
		return MatchCriteria{Kind: MatchNative, Evidence: key}, true
	}},
}

type MatchKind uint8

const (
	MatchPath MatchKind = iota + 1
	MatchSignature
	MatchNative
)

// MatchCriteria contains one deterministic continuity rule. Each durable manifest maps
// these facts to the columns it owns instead of sharing an implicit SQL row shape.
type MatchCriteria struct {
	Kind      MatchKind
	Path      string
	Signature []byte
	Evidence  Evidence
}

// MatchCandidate contains only the observed facts needed to claim one manifest row.
type MatchCandidate struct {
	ID   int64
	Path string
	Size int64
	Hash []byte
}

// MatchStore is the persistence seam between continuity policy and an owning manifest.
// Implementations scope every operation to the same staged observation set.
type MatchStore interface {
	FileClaimed(context.Context, int64) (bool, error)
	FindCandidate(context.Context, MatchCriteria) (*MatchCandidate, error)
	Assign(context.Context, int64, int64, []byte) error
}

// Match assigns existing File identities only after the caller stages the complete eligible scope.
// The caller excludes unobserved, inaccessible and otherwise ineligible original associations.
type Candidates func(context.Context, int64, int) ([]*Original, error)

func Match(ctx context.Context, store MatchStore, candidates Candidates, logger logrus.FieldLogger) error {
	// Consume one old File and one current observation at a time in stable, bounded global rounds.
	for _, rule := range relocationMatchers {
		var after int64
		for {
			old, err := candidates(ctx, after, BatchSize)
			if err != nil {
				return err
			}
			if len(old) == 0 {
				break
			}
			for _, original := range old {
				claimed, err := store.FileClaimed(ctx, original.FileID)
				if err != nil {
					return err
				}
				if claimed {
					continue
				}
				criteria, ok := rule.criteria(original)
				if !ok {
					continue
				}
				item, err := store.FindCandidate(ctx, criteria)
				if err != nil {
					return err
				}
				if item == nil {
					continue
				}

				// A producer-generated opaque signature survives relocation when the observed content agrees.
				var signature []byte
				if len(original.Signature) != 0 && len(original.Hash) == 32 && original.Size == item.Size && bytes.Equal(original.Hash, item.Hash) {
					signature = original.Signature
				}
				if err := store.Assign(ctx, item.ID, original.FileID, signature); err != nil {
					return err
				}
				if logger != nil {
					logger.WithFields(logrus.Fields{"file_id": original.FileID, "path": item.Path, "matched_by": rule.name}).Debug("matched original")
				}
			}
			after = old[len(old)-1].FileID
		}
	}
	return nil
}

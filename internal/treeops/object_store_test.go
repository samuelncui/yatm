package treeops

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
)

// The fake stores flat object keys. Directory rows are virtual prefix observations,
// their IDs are not inode numbers, and moving a prefix has no native primitive.
type objectStore struct {
	nodes       map[string]Node
	beforeApply func(Primitive)
	applied     []Primitive
	pages       int
}

func objectRef(key string, directory bool) string {
	if directory {
		return "prefix:" + key
	}
	return "object:" + key
}

func (s *objectStore) add(key string, directory bool) Node {
	parent := path.Dir(key)
	if parent == "." {
		parent = ""
	}
	node := Node{Ref: objectRef(key, directory), Parent: objectRef(parent, true), Name: path.Base(key), Path: key,
		Directory: directory, Exists: true, Guard: []byte("version:" + key)}
	if key == "" {
		node.Parent = ""
	}
	s.nodes[node.Ref] = node
	return node
}

func (s *objectStore) Stat(_ context.Context, ref string) (Node, error) { return s.nodes[ref], nil }

func (s *objectStore) ListChildren(_ context.Context, node Node, cursor string) ([]Node, string, error) {
	// Tiny pages prove the shared traversal cannot assume one complete listing.
	s.pages++
	var refs []string
	for ref, candidate := range s.nodes {
		if candidate.Parent == node.Ref && ref > cursor {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	var next string
	if len(refs) > 2 {
		refs = refs[:2]
		next = refs[len(refs)-1]
	}
	result := make([]Node, 0, len(refs))
	for _, ref := range refs {
		result = append(result, s.nodes[ref])
	}
	return result, next, nil
}

func (s *objectStore) ResolveChild(_ context.Context, parent Node, name string, directory bool) (Node, error) {
	key := name
	if parent.Path != "" {
		key = parent.Path + "/" + name
	}
	ref := objectRef(key, directory)
	if existing, found := s.nodes[ref]; found {
		return existing, nil
	}
	return Node{Ref: ref, Parent: parent.Ref, Name: name, Path: key, Directory: directory}, nil
}

func (s *objectStore) ApplyPrimitive(_ context.Context, p Primitive) (Receipt, error) {
	// A fake conditional write fails if a concurrent object appears; it never overwrites.
	if s.beforeApply != nil {
		s.beforeApply(p)
	}
	if p.Kind == Move && p.Source.Directory {
		return Receipt{}, fmt.Errorf("native prefix move is unsupported")
	}
	if p.Kind == Move || p.Kind == Mkdir {
		if _, found := s.nodes[p.Target.Ref]; found {
			return Receipt{}, fmt.Errorf("conditional target conflict")
		}
	}
	if p.Kind == RemoveEmpty {
		for _, child := range s.nodes {
			if child.Parent == p.Source.Ref {
				return Receipt{}, fmt.Errorf("prefix is not empty")
			}
		}
		delete(s.nodes, p.Source.Ref)
		return Receipt{Changed: true, Node: s.nodes[p.Target.Ref]}, nil
	}
	if p.Kind == Delete {
		delete(s.nodes, p.Source.Ref)
		return Receipt{Changed: true}, nil
	}

	// The single-object move represents a guarded copy followed by source deletion.
	target := p.Target
	target.Exists, target.Guard = true, []byte("version:"+target.Path)
	s.nodes[target.Ref] = target
	if p.Kind == Move {
		delete(s.nodes, p.Source.Ref)
	}
	s.applied = append(s.applied, p)
	return Receipt{Node: target, Changed: true}, nil
}

func objectEngine(t *testing.T, store *objectStore) *Engine {
	t.Helper()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "plan.db"))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	engine, err := New(context.Background(), db, store, Options{}, nil)
	require.NoError(t, err)
	return engine
}

func TestObjectStorageContractUsesCommonPagedMerge(t *testing.T) {
	// The source contains an object and a virtual prefix sharing the same display name.
	s := &objectStore{nodes: make(map[string]Node)}
	s.add("", true)
	s.add("from", true)
	s.add("to", true)
	s.add("from/a", false)
	s.add("from/a", true)
	s.add("from/a/child", false)
	s.add("from/b", false)
	s.add("from/c", false)
	e := objectEngine(t, s)
	require.NoError(t, e.Prepare(context.Background(), Request{Kind: Move, Sources: []string{"prefix:from"}, Destination: "prefix:to"}))
	var outcomes []Result
	require.NoError(t, e.Run(context.Background(), func(result Result) error { outcomes = append(outcomes, result); return nil }))

	// Prefix moves are decomposed, object identities stay independent, and all pages are consumed.
	require.Greater(t, s.pages, 2)
	for _, result := range outcomes {
		require.Equal(t, Succeeded, result.Outcome, result.Error)
	}
	for _, ref := range []string{"object:to/from/a", "prefix:to/from/a", "object:to/from/a/child", "object:to/from/c"} {
		require.Contains(t, s.nodes, ref)
	}
	require.NotContains(t, s.nodes, "prefix:from")
	for _, primitive := range s.applied {
		require.False(t, primitive.Kind == Move && primitive.Source.Directory)
	}
}

func TestObjectConditionalConflictPreservesPartialResults(t *testing.T) {
	s := &objectStore{nodes: make(map[string]Node)}
	s.add("", true)
	s.add("from", true)
	s.add("to", true)
	s.add("from/a", false)
	s.add("from/b", false)
	e := objectEngine(t, s)
	require.NoError(t, e.Prepare(context.Background(), Request{Kind: Move, Sources: []string{"prefix:from"}, Destination: "prefix:to"}))
	s.beforeApply = func(p Primitive) {
		if p.Kind == Move && strings.HasSuffix(p.Source.Ref, "/b") {
			s.add("to/from/b", false)
		}
	}
	var outcomes []Result
	require.NoError(t, e.Run(context.Background(), func(result Result) error { outcomes = append(outcomes, result); return nil }))

	// The first object remains moved, the raced object stays at source, and the prefix is not deleted.
	require.Contains(t, s.nodes, "object:to/from/a")
	require.Contains(t, s.nodes, "object:from/b")
	require.Contains(t, s.nodes, "prefix:from")
	require.Equal(t, Failed, outcomes[len(outcomes)-2].Outcome)
	require.Equal(t, Unprocessed, outcomes[len(outcomes)-1].Outcome)
}

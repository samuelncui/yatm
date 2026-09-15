// Package treeops owns file organization independently of a storage provider.
package treeops

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

const PageSize = 128

// Node references and guards belong to the provider; Path is only a display value.
// Absent destination nodes have a usable Ref without pretending to have identity.
type Node struct {
	Ref, Parent, Name, Path string
	Directory, Exists       bool
	Protected               bool
	Deletion                Deletion
	Guard                   []byte
	ParentGuard             []byte
	FileID, Size            int64
}

type Kind string

// Deletion expresses the provider's retention contract; traversal remains shared.
type Deletion string

const (
	Detach           Deletion = "detach"
	Retain           Deletion = "retain"
	EmptyDirectories Deletion = "empty_directories"
)

const (
	Move        Kind = "move"
	Mkdir       Kind = "mkdir"
	Delete      Kind = "delete"
	RemoveEmpty Kind = "remove_empty"
)

// Primitive performs one guarded change, never a recursive merge or traversal.
type Primitive struct {
	ID             int64
	Kind           Kind
	Source, Target Node
}

// Receipt describes the side-effect boundary even if metadata publication failed.
type Receipt struct {
	Node               Node
	Changed            bool
	PublicationPending bool
}

// Store implements storage primitives, not organization policy. ListChildren must
// return bounded pages and must not follow symbolic links or perform admission.
type Store interface {
	Stat(context.Context, string) (Node, error)
	ListChildren(context.Context, Node, string) ([]Node, string, error)
	ResolveChild(context.Context, Node, string, bool) (Node, error)
	ApplyPrimitive(context.Context, Primitive) (Receipt, error)
}

// Options describe unavoidable storage semantics, not alternative algorithms.
type Options struct {
	NativeMove bool
}

// Transaction is supplied only for a metadata store. Filesystem execution never
// enters it and never promises rollback of an already completed primitive.
type Transaction func(context.Context, func(Store) error) error

type Request struct {
	Kind        Kind
	Sources     []string
	Destination string
	Name        string
}

// Root is one deduplicated selection; descendant observations remain in the manifest.
type Root struct {
	ID     int64 `gorm:"primaryKey"`
	Source Node  `gorm:"serializer:json;type:blob"`
	Target Node  `gorm:"serializer:json;type:blob"`
	Error  string
}

type step struct {
	ID        int64 `gorm:"primaryKey"`
	RootID    int64 `gorm:"index"`
	Kind      Kind
	Source    Node `gorm:"serializer:json;type:blob"`
	Target    Node `gorm:"serializer:json;type:blob"`
	CheckOnly bool
	Size      int64
	TargetRef string `gorm:"index"`
	Outcome   string
	Error     string
	Result    Node `gorm:"serializer:json;type:blob"`
}

// Result describes a settled primitive or explicitly unprocessed planned entry.
type Result struct {
	ID             int64
	Source, Target Node
	FileID         int64
	Outcome, Error string
}

const (
	Succeeded          = "succeeded"
	Failed             = "failed"
	PublicationPending = "publication_pending"
	Unprocessed        = "unprocessed"
)

// Engine keeps the bounded temporary manifest outside either source namespace.
type Engine struct {
	db          *gorm.DB
	store       Store
	options     Options
	transaction Transaction
}

func New(ctx context.Context, db *gorm.DB, store Store, options Options, transaction Transaction) (*Engine, error) {
	if err := db.WithContext(ctx).AutoMigrate(&Root{}, &step{}); err != nil {
		return nil, fmt.Errorf("create organization manifest failed, %w", err)
	}
	return &Engine{db: db, store: store, options: options, transaction: transaction}, nil
}

// relativeNames parses the shared relative-path shortcut, never a provider key.
func relativeNames(value string, allowCurrent bool) ([]string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if strings.HasPrefix(value, "/") || strings.ContainsRune(value, 0) {
		return nil, fmt.Errorf("name must be a relative path")
	}
	var names []string
	for _, name := range strings.Split(value, "/") {
		if name == ".." {
			return nil, fmt.Errorf("parent path traversal is not allowed")
		}
		if name != "" && name != "." {
			names = append(names, name)
		}
	}
	if len(names) == 0 && !allowCurrent {
		return nil, fmt.Errorf("name is empty")
	}
	if len(names) > 256 {
		return nil, fmt.Errorf("relative path exceeds 256 levels")
	}
	return names, nil
}

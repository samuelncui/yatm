package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/samuelncui/yatm/entity"
)

func (c *filesListCommand) directory(ctx context.Context) (*entity.FileOperationRef, error) {
	// Preserve explicit ID selection and the existing bare-root invocation.
	if c.Args.Path == nil {
		if c.FileID == nil && c.LocationID == nil && c.Path == "" {
			return fileReference(0), nil
		}
		return c.reference()
	}
	if c.FileID != nil || c.LocationID != nil || c.Path != "" {
		return nil, usageError(fmt.Errorf("PATH cannot be combined with --file-id, --location-id or --path"))
	}

	// Resolve the namespace once; an absent path never falls back to another source.
	name, relative, err := parseDirectoryPath(*c.Args.Path)
	if err != nil {
		return nil, usageError(err)
	}
	if name != "" {
		id, err := resolveLocationName(ctx, c.runtime, name)
		if err != nil {
			return nil, err
		}
		return locationReference(id, relative), nil
	}
	return resolveLibraryPath(ctx, c.runtime, relative)
}

// parseDirectoryPath returns an optional Location name and a root-relative path.
func parseDirectoryPath(value string) (string, string, error) {
	// URI syntax selects a source; plain paths preserve literal percent and punctuation characters.
	if value == "" {
		return "", "", fmt.Errorf("PATH must not be empty; omit it to list the Library root")
	}
	name, relative := "", value
	scheme, rest, uri := strings.Cut(value, "://")
	uri = uri && !strings.Contains(scheme, "/")
	if uri {
		if strings.ContainsAny(rest, "?#") {
			return "", "", fmt.Errorf("PATH URI has a query or fragment; encode literal ? and # as %%3F and %%23")
		}
		switch strings.ToLower(scheme) {
		case "library":
			if !strings.HasPrefix(rest, "/") {
				return "", "", fmt.Errorf("Library URI must use library:///path without an authority")
			}
			relative = rest
		case "location":
			name, relative, _ = strings.Cut(rest, "/")
			decoded, err := url.PathUnescape(name)
			if err != nil {
				return "", "", fmt.Errorf("decode Location name failed, %w", err)
			}
			name = decoded
			if strings.TrimSpace(name) == "" || name == "." || name == ".." ||
				strings.ContainsAny(name, "/\\\x00") || !utf8.ValidString(name) {
				return "", "", fmt.Errorf("Location URI requires a valid full Location name")
			}
		default:
			return "", "", fmt.Errorf("unsupported PATH scheme %q; use library:///path or location://NAME/path", scheme)
		}
		decoded, err := url.PathUnescape(relative)
		if err != nil {
			return "", "", fmt.Errorf("decode PATH failed, %w", err)
		}
		relative = decoded
	}

	// Normalize within the selected root, rejecting escape instead of clamping it to that root.
	var parts []string
	for _, part := range strings.Split(relative, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(parts) == 0 {
				return "", "", fmt.Errorf("PATH escapes the selected root")
			}
			parts = parts[:len(parts)-1]
		default:
			if err := entity.ValidatePathComponent(part); err != nil {
				return "", "", fmt.Errorf("invalid PATH, %w", err)
			}
			parts = append(parts, part)
			if len(parts) > 256 {
				return "", "", fmt.Errorf("PATH exceeds 256 directory levels")
			}
		}
	}
	return name, strings.Join(parts, "/"), nil
}

func resolveLocationName(ctx context.Context, rt *runtime, name string) (int64, error) {
	// Enumerate names without a text filter: database case folding can omit an exact Unicode match.
	// Check every page because Location display names are not unique.
	var id, after int64
	for {
		reply, err := callRPC[entity.ListLocationsRequest, entity.ListLocationsResponse](ctx, rt,
			entity.LocationService_List_FullMethodName,
			&entity.ListLocationsRequest{Limit: 100, AfterId: after})
		if err != nil {
			return 0, err
		}
		for _, location := range reply.Locations {
			if location.Name != name {
				continue
			}
			if id != 0 {
				return 0, usageError(fmt.Errorf("Location name %q is ambiguous; use --location-id ID --path PATH", name))
			}
			id = location.Id
		}
		if !reply.HasMore {
			break
		}
		if len(reply.Locations) == 0 || reply.Locations[len(reply.Locations)-1].Id <= after {
			return 0, runtimeError("internal", fmt.Errorf("Location listing did not advance while resolving %q", name))
		}
		after = reply.Locations[len(reply.Locations)-1].Id
	}

	// An unmatched name is an error even when another source has a directory of that name.
	if id == 0 {
		return 0, runtimeError("not_found", fmt.Errorf("Location %q was not found", name))
	}
	return id, nil
}

func resolveLibraryPath(ctx context.Context, rt *runtime, relative string) (*entity.FileOperationRef, error) {
	// Resolve each exact directory component using existing bounded reads, without loading file details.
	ref := fileReference(0)
	if relative == "" {
		return ref, nil
	}
	for _, name := range strings.Split(relative, "/") {
		child, err := resolveLibraryDirectory(ctx, rt, ref, name)
		if err != nil {
			return nil, fmt.Errorf("resolve Library path %q failed, %w", "/"+relative, err)
		}
		ref = child
	}
	return ref, nil
}

func resolveLibraryDirectory(
	ctx context.Context, rt *runtime, parent *entity.FileOperationRef, name string,
) (*entity.FileOperationRef, error) {
	// Directory-only pages avoid interpreting names as query syntax or wildcards.
	// The catalog's unique parent/name invariant permits returning the exact match immediately.
	var cursor string
	for {
		reply, err := callRPC[entity.SearchFilesRequest, entity.SearchFilesResponse](ctx, rt,
			entity.FilesService_Search_FullMethodName, &entity.SearchFilesRequest{
				Directory: parent, Scope: entity.FileScope_FILE_SCOPE_ALL, Query: "type:dir", Limit: 500, Cursor: cursor,
			})
		if err != nil {
			return nil, err
		}
		for _, entry := range reply.Entries {
			if entry.Name != name || entry.Kind != entity.EntryKind_ENTRY_KIND_DIRECTORY {
				continue
			}
			id, ok := entry.GetReference().GetTarget().(*entity.FileOperationRef_FileId)
			if !ok || id.FileId == 0 || id.FileId < trashFileID {
				return nil, runtimeError("internal", fmt.Errorf("Library directory %q has no valid File reference", name))
			}
			return entry.Reference, nil
		}
		if reply.NextCursor == "" {
			return nil, runtimeError("not_found", fmt.Errorf("Library directory %q was not found", name))
		}
		if reply.NextCursor == cursor {
			return nil, runtimeError("internal", fmt.Errorf("Library search did not advance while resolving %q", name))
		}
		cursor = reply.NextCursor
	}
}

package demo

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
)

func seedDuplicateOriginals(ctx context.Context, lib *library.Library, exe *executor.Executor) error {
	// Use a real synchronized directory for the three- and four-member content groups.
	groups := []struct {
		paths   []string
		content string
	}{
		{[]string{"notes/team.txt", "notes/team-copy.txt", "notes/team-final.txt"}, "Weekly team meeting notes.\n"},
		{
			[]string{"checklist.txt", "checklist-copy.txt", "checklist-backup.txt", "checklist-final.txt"},
			"Brand asset checklist.\n",
		},
	}
	var files []fixtureFile
	for _, group := range groups {
		for _, path := range group.paths {
			files = append(files, fixtureFile{path: path, content: []byte(group.content)})
		}
	}
	location, err := seedOnlineSource(ctx, exe, "Shared files", "Shared", files, nil)
	if err != nil {
		return err
	}
	if err := seedAnalyze(ctx, exe, location.ID, entity.JobStatus_COMPLETED); err != nil {
		return err
	}

	// Tag all nine members, including the two-member group spanning an unavailable Location.
	paths := []string{
		"Unforged/Documents/online-only.txt",
		"Unforged/Unavailable originals/copy-of-online-only.txt",
	}
	for _, file := range files {
		paths = append(paths, "Unforged/Shared files/"+file.path)
	}
	ids := make([]int64, 0, len(paths))
	for _, path := range paths {
		file, err := lib.GetByPath(ctx, library.Root.ID, path)
		if err != nil {
			return fmt.Errorf("find Demo duplicate %q failed, %w", path, err)
		}
		if file == nil {
			return fmt.Errorf("Demo duplicate %q is missing", path)
		}
		ids = append(ids, file.ID)
	}
	if err := lib.EditFileMetadata(ctx, ids, library.FileMetadataEdit{AddTags: []string{"duplicate-review"}}); err != nil {
		return fmt.Errorf("tag Demo duplicates failed, %w", err)
	}
	return nil
}

package apis

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/abc950309/tapewriter/entity"
)

func (api *API) SourceList(ctx context.Context, req *entity.SourceListRequest) (*entity.SourceListReply, error) {
	if req.Path == "./" {
		req.Path = ""
	}

	parts := strings.Split(req.Path, "/")
	filteredParts := make([]string, 1, len(parts)+1)
	filteredParts[0] = ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		filteredParts = append(filteredParts, part)
	}

	// buf, _ := json.Marshal(filteredParts)
	// logrus.WithContext(ctx).Infof("parts= %s", buf)

	current := ""
	chain := make([]*entity.SourceFile, 0, len(filteredParts))
	for _, part := range filteredParts {
		p := path.Join(api.sourceBase, current, part)

		stat, err := os.Stat(p)
		if err != nil {
			return nil, err
		}

		files := convertSourceFiles(current, stat)
		if len(files) == 0 {
			return nil, fmt.Errorf("unexpected file, %s", current+part)
		}

		file := files[0]
		chain = append(chain, file)

		if !fs.FileMode(file.Mode).IsDir() {
			break
		}

		current = path.Join(current, part)
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("unexpected file, '%s'", req.Path)
	}

	chain[0].Path = "./"
	chain[0].Name = "Root"
	file := chain[len(chain)-1]
	reply := &entity.SourceListReply{
		File:  file,
		Chain: chain,
	}
	if !fs.FileMode(file.Mode).IsDir() {
		return reply, nil
	}

	dir := path.Join(api.sourceBase, req.Path)
	children, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	infos := make([]fs.FileInfo, 0, len(children))
	for _, child := range children {
		info, err := child.Info()
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}

	reply.Children = convertSourceFiles(req.Path, infos...)
	return reply, nil
}

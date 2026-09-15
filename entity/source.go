package entity

import (
	"path"
	"strings"

	"github.com/samuelncui/acp"
)

func NewSourceFromACPJob(job *acp.Job) *Source {
	return &Source{Base: job.Base, Path: strings.Split(job.Path, "/")}
}

func (x *Source) RealPath() string {
	parts := append([]string{x.Base}, x.Path...)
	return path.Join(parts...)
}

func (x *Source) Append(next ...string) *Source {
	parts := make([]string, 0, len(x.Path)+len(next))
	parts = append(parts, x.Path...)
	parts = append(parts, next...)
	return &Source{Base: x.Base, Path: parts}
}

func (x *Source) Compare(xx *Source) int {
	return strings.Compare(strings.Join(x.Path, "\x00"), strings.Join(xx.Path, "\x00"))
}

func (x *Source) Equal(xx *Source) bool {
	if x.Base != xx.Base || len(x.Path) != len(xx.Path) {
		return false
	}
	for index := range x.Path {
		if x.Path[index] != xx.Path[index] {
			return false
		}
	}
	return true
}

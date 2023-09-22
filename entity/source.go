package entity

import (
	"path"

	"github.com/samuelncui/acp"
)

func NewSourceFromACPJob(job *acp.Job) *Source {
	return &Source{Base: job.Base, Path: job.Path}
}

func (x *Source) RealPath() string {
	p := make([]string, 0, len(x.Path)+1)
	p = append(p, x.Base)
	p = append(p, x.Path...)
	return path.Join(p...)
}

func (x *Source) Append(more ...string) *Source {
	path := make([]string, len(x.Path)+len(more))
	copy(path, x.Path)
	copy(path[len(x.Path):], more)

	return &Source{Base: x.Base, Path: path}
}

func (x *Source) Compare(xx *Source) int {
	la, lb := len(x.Path), len(x.Path)

	l := la
	if lb < la {
		l = lb
	}

	for idx := 0; idx < l; idx++ {
		if x.Path[idx] < xx.Path[idx] {
			return -1
		}
		if x.Path[idx] > xx.Path[idx] {
			return 1
		}
	}

	if la < lb {
		return -1
	}
	if la > lb {
		return 1
	}

	if x.Base < xx.Base {
		return -1
	}
	if x.Base > xx.Base {
		return -1
	}

	return 0
}

func (x *Source) Equal(xx *Source) bool {
	la, lb := len(x.Path), len(x.Path)
	if la != lb {
		return false
	}

	for idx := 0; idx < la; idx++ {
		if x.Path[idx] != xx.Path[idx] {
			return false
		}
	}

	return true
}

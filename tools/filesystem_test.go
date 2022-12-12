package tools

import (
	"testing"

	"github.com/davecgh/go-spew/spew"
)

func TestGetFileSystem(t *testing.T) {
	fs, err := GetFileSystem("/")
	if err != nil {
		panic(err)
	}

	t.Log(spew.Sdump(fs))
}

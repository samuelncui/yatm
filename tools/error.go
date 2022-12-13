package tools

import (
	"github.com/hashicorp/go-multierror"
)

func AppendError(err, e error) error {
	if err != nil {
		return multierror.Append(err, e)
	}
	return e
}

package pb

import (
	"database/sql/driver"

	"github.com/samuelncui/yatm/entity"
)

func (x *JobState) Scan(src any) error {
	return entity.Scan(x, src)
}

func (x *JobState) Value() (driver.Value, error) {
	return entity.Value(x)
}

package executor

import (
	"context"

	"github.com/modern-go/reflect2"
	"github.com/samuelncui/yatm/entity"
	"github.com/sirupsen/logrus"
)

var (
	jobParamToTypes = map[uintptr]uintptr{
		reflect2.RTypeOf(&entity.JobParam_Archive{}): reflect2.RTypeOf(&entity.JobState_Archive{}),
		reflect2.RTypeOf(&entity.JobParam_Restore{}): reflect2.RTypeOf(&entity.JobState_Restore{}),
	}
	jobTypes = map[uintptr]JobType{
		reflect2.RTypeOf(&entity.JobState_Archive{}): new(jobTypeArchive),
		reflect2.RTypeOf(&entity.JobState_Restore{}): new(jobTypeRestore),
	}
)

type JobType interface {
	GetExecutor(ctx context.Context, exe *Executor, job *Job) (JobExecutor, error)
}

type JobExecutor interface {
	Initialize(ctx context.Context, param *entity.JobParam) error
	Dispatch(ctx context.Context, param *entity.JobDispatchParam) error
	Display(ctx context.Context) (*entity.JobDisplay, error)
	Close(ctx context.Context) error

	Logger() *logrus.Logger
}

package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
	"github.com/sirupsen/logrus"
)

func (api *API) JobDisplay(ctx context.Context, req *entity.JobDisplayRequest) (*entity.JobDisplayReply, error) {
	job, err := api.exe.GetJob(ctx, req.Id)
	if err != nil {
		logrus.WithContext(ctx).WithError(err).Infof("get job fail, job_id= %d", req.Id)
		return &entity.JobDisplayReply{}, nil
	}

	result, err := api.exe.Display(ctx, job)
	if err != nil {
		logrus.WithContext(ctx).WithError(err).Infof("get job display fail, job_id= %d", req.Id)
		return &entity.JobDisplayReply{}, nil
	}

	return &entity.JobDisplayReply{Display: result}, nil
}

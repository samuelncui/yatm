package apis

import (
	"context"
	"fmt"

	"github.com/abc950309/tapewriter/entity"
	"github.com/abc950309/tapewriter/library"
	"github.com/samber/lo"
)

func (api *API) TapeList(ctx context.Context, req *entity.TapeListRequest) (*entity.TapeListReply, error) {
	tapes, err := func() ([]*library.Tape, error) {
		switch v := req.GetParam().(type) {
		case *entity.TapeListRequest_List:
			return api.lib.ListTape(ctx, v.List)
		case *entity.TapeListRequest_Mget:
			m, err := api.lib.MGetTape(ctx, v.Mget.GetIds()...)
			if err != nil {
				return nil, err
			}

			return lo.Values(m), nil
		default:
			return nil, fmt.Errorf("unexpected list tape param, %T", req.GetParam())
		}
	}()
	if err != nil {
		return nil, err
	}

	converted := make([]*entity.Tape, 0, len(tapes))
	for _, tape := range tapes {
		converted = append(converted, &entity.Tape{
			Id:            tape.ID,
			Barcode:       tape.Barcode,
			Name:          tape.Name,
			Encryption:    tape.Encryption,
			CreateTime:    tape.CreateTime.Unix(),
			DestroyTime:   convertOptionalTime(tape.DestroyTime),
			CapacityBytes: tape.CapacityBytes,
			WritenBytes:   tape.WritenBytes,
		})
	}

	return &entity.TapeListReply{Tapes: converted}, nil
}

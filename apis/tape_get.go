package apis

import (
	"context"

	"github.com/abc950309/tapewriter/entity"
)

func (api *API) TapeMGet(ctx context.Context, req *entity.TapeMGetRequest) (*entity.TapeMGetReply, error) {
	tapes, err := api.lib.MGetTape(ctx, req.Ids...)
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

	return &entity.TapeMGetReply{Tapes: converted}, nil
}

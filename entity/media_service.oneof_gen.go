package entity

type OneofInspectMediaRequest interface {
	isOneofInspectMediaRequest()
}

func (x *InspectMediaTapeTarget) isOneofInspectMediaRequest() {}

func (x *InspectMediaVolumeTarget) isOneofInspectMediaRequest() {}

func (x *InspectMediaRequest) Unpack() OneofInspectMediaRequest {
	switch param := x.Target.(type) {
	case *InspectMediaRequest_Tape:
		return param.Tape
	case *InspectMediaRequest_Volume:
		return param.Volume
	default:
		return nil
	}
}

func PackInspectMediaRequest(param OneofInspectMediaRequest) *InspectMediaRequest {
	switch param := param.(type) {
	case *InspectMediaTapeTarget:
		return &InspectMediaRequest{
			Target: &InspectMediaRequest_Tape{
				Tape: param,
			},
		}
	case *InspectMediaVolumeTarget:
		return &InspectMediaRequest{
			Target: &InspectMediaRequest_Volume{
				Volume: param,
			},
		}
	default:
		return nil
	}
}

func (p *InspectMediaTapeTarget) Pack() *InspectMediaRequest {
	return &InspectMediaRequest{
		Target: &InspectMediaRequest_Tape{
			Tape: p,
		},
	}
}

func (p *InspectMediaTapeTarget) ToOneof() OneofInspectMediaRequest {
	return p
}

func (p *InspectMediaVolumeTarget) Pack() *InspectMediaRequest {
	return &InspectMediaRequest{
		Target: &InspectMediaRequest_Volume{
			Volume: p,
		},
	}
}

func (p *InspectMediaVolumeTarget) ToOneof() OneofInspectMediaRequest {
	return p
}

type OneofListMediaRequest interface {
	isOneofListMediaRequest()
}

func (x *MediaIds) isOneofListMediaRequest() {}

func (x *MediaFilter) isOneofListMediaRequest() {}

func (x *ListMediaRequest) Unpack() OneofListMediaRequest {
	switch param := x.Param.(type) {
	case *ListMediaRequest_Ids:
		return param.Ids
	case *ListMediaRequest_List:
		return param.List
	default:
		return nil
	}
}

func PackListMediaRequest(param OneofListMediaRequest) *ListMediaRequest {
	switch param := param.(type) {
	case *MediaIds:
		return &ListMediaRequest{
			Param: &ListMediaRequest_Ids{
				Ids: param,
			},
		}
	case *MediaFilter:
		return &ListMediaRequest{
			Param: &ListMediaRequest_List{
				List: param,
			},
		}
	default:
		return nil
	}
}

func (p *MediaIds) Pack() *ListMediaRequest {
	return &ListMediaRequest{
		Param: &ListMediaRequest_Ids{
			Ids: p,
		},
	}
}

func (p *MediaIds) ToOneof() OneofListMediaRequest {
	return p
}

func (p *MediaFilter) Pack() *ListMediaRequest {
	return &ListMediaRequest{
		Param: &ListMediaRequest_List{
			List: p,
		},
	}
}

func (p *MediaFilter) ToOneof() OneofListMediaRequest {
	return p
}

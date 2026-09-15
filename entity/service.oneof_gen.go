package entity

type OneofMediaInspectRequest interface {
	isOneofMediaInspectRequest()
}

func (x *MediaInspectTapeTarget) isOneofMediaInspectRequest() {}

func (x *MediaInspectVolumeTarget) isOneofMediaInspectRequest() {}

func (x *MediaInspectRequest) Unpack() OneofMediaInspectRequest {
	switch param := x.Target.(type) {
	case *MediaInspectRequest_Tape:
		return param.Tape
	case *MediaInspectRequest_Volume:
		return param.Volume
	default:
		return nil
	}
}

func PackMediaInspectRequest(param OneofMediaInspectRequest) *MediaInspectRequest {
	switch param := param.(type) {
	case *MediaInspectTapeTarget:
		return &MediaInspectRequest{
			Target: &MediaInspectRequest_Tape{
				Tape: param,
			},
		}
	case *MediaInspectVolumeTarget:
		return &MediaInspectRequest{
			Target: &MediaInspectRequest_Volume{
				Volume: param,
			},
		}
	default:
		return nil
	}
}

func (p *MediaInspectTapeTarget) Pack() *MediaInspectRequest {
	return &MediaInspectRequest{
		Target: &MediaInspectRequest_Tape{
			Tape: p,
		},
	}
}

func (p *MediaInspectTapeTarget) ToOneof() OneofMediaInspectRequest {
	return p
}

func (p *MediaInspectVolumeTarget) Pack() *MediaInspectRequest {
	return &MediaInspectRequest{
		Target: &MediaInspectRequest_Volume{
			Volume: p,
		},
	}
}

func (p *MediaInspectVolumeTarget) ToOneof() OneofMediaInspectRequest {
	return p
}

type OneofMediaListRequest interface {
	isOneofMediaListRequest()
}

func (x *MediaFilter) isOneofMediaListRequest() {}

func (x *MediaMGetRequest) isOneofMediaListRequest() {}

func (x *MediaListRequest) Unpack() OneofMediaListRequest {
	switch param := x.Param.(type) {
	case *MediaListRequest_List:
		return param.List
	case *MediaListRequest_Mget:
		return param.Mget
	default:
		return nil
	}
}

func PackMediaListRequest(param OneofMediaListRequest) *MediaListRequest {
	switch param := param.(type) {
	case *MediaFilter:
		return &MediaListRequest{
			Param: &MediaListRequest_List{
				List: param,
			},
		}
	case *MediaMGetRequest:
		return &MediaListRequest{
			Param: &MediaListRequest_Mget{
				Mget: param,
			},
		}
	default:
		return nil
	}
}

func (p *MediaFilter) Pack() *MediaListRequest {
	return &MediaListRequest{
		Param: &MediaListRequest_List{
			List: p,
		},
	}
}

func (p *MediaFilter) ToOneof() OneofMediaListRequest {
	return p
}

func (p *MediaMGetRequest) Pack() *MediaListRequest {
	return &MediaListRequest{
		Param: &MediaListRequest_Mget{
			Mget: p,
		},
	}
}

func (p *MediaMGetRequest) ToOneof() OneofMediaListRequest {
	return p
}

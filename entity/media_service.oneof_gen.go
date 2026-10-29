package entity

type OneofDeviceFeature interface {
	isOneofDeviceFeature()
}

func (x *TapeCapacityLimits) isOneofDeviceFeature() {}

func (x *TapeCompressionLimits) isOneofDeviceFeature() {}

func (x *TapeEncryptionLimits) isOneofDeviceFeature() {}

func (x *TapeGeometryLimits) isOneofDeviceFeature() {}

func (x *TapeNavigationLimits) isOneofDeviceFeature() {}

func (x *TapeRecordLimits) isOneofDeviceFeature() {}

func (x *DeviceFeature) Unpack() OneofDeviceFeature {
	switch param := x.Limits.(type) {
	case *DeviceFeature_Capacity:
		return param.Capacity
	case *DeviceFeature_Compression:
		return param.Compression
	case *DeviceFeature_Encryption:
		return param.Encryption
	case *DeviceFeature_Geometry:
		return param.Geometry
	case *DeviceFeature_Navigation:
		return param.Navigation
	case *DeviceFeature_Records:
		return param.Records
	default:
		return nil
	}
}

func PackDeviceFeature(param OneofDeviceFeature) *DeviceFeature {
	switch param := param.(type) {
	case *TapeCapacityLimits:
		return &DeviceFeature{
			Limits: &DeviceFeature_Capacity{
				Capacity: param,
			},
		}
	case *TapeCompressionLimits:
		return &DeviceFeature{
			Limits: &DeviceFeature_Compression{
				Compression: param,
			},
		}
	case *TapeEncryptionLimits:
		return &DeviceFeature{
			Limits: &DeviceFeature_Encryption{
				Encryption: param,
			},
		}
	case *TapeGeometryLimits:
		return &DeviceFeature{
			Limits: &DeviceFeature_Geometry{
				Geometry: param,
			},
		}
	case *TapeNavigationLimits:
		return &DeviceFeature{
			Limits: &DeviceFeature_Navigation{
				Navigation: param,
			},
		}
	case *TapeRecordLimits:
		return &DeviceFeature{
			Limits: &DeviceFeature_Records{
				Records: param,
			},
		}
	default:
		return nil
	}
}

func (p *TapeCapacityLimits) Pack() *DeviceFeature {
	return &DeviceFeature{
		Limits: &DeviceFeature_Capacity{
			Capacity: p,
		},
	}
}

func (p *TapeCapacityLimits) ToOneof() OneofDeviceFeature {
	return p
}

func (p *TapeCompressionLimits) Pack() *DeviceFeature {
	return &DeviceFeature{
		Limits: &DeviceFeature_Compression{
			Compression: p,
		},
	}
}

func (p *TapeCompressionLimits) ToOneof() OneofDeviceFeature {
	return p
}

func (p *TapeEncryptionLimits) Pack() *DeviceFeature {
	return &DeviceFeature{
		Limits: &DeviceFeature_Encryption{
			Encryption: p,
		},
	}
}

func (p *TapeEncryptionLimits) ToOneof() OneofDeviceFeature {
	return p
}

func (p *TapeGeometryLimits) Pack() *DeviceFeature {
	return &DeviceFeature{
		Limits: &DeviceFeature_Geometry{
			Geometry: p,
		},
	}
}

func (p *TapeGeometryLimits) ToOneof() OneofDeviceFeature {
	return p
}

func (p *TapeNavigationLimits) Pack() *DeviceFeature {
	return &DeviceFeature{
		Limits: &DeviceFeature_Navigation{
			Navigation: p,
		},
	}
}

func (p *TapeNavigationLimits) ToOneof() OneofDeviceFeature {
	return p
}

func (p *TapeRecordLimits) Pack() *DeviceFeature {
	return &DeviceFeature{
		Limits: &DeviceFeature_Records{
			Records: p,
		},
	}
}

func (p *TapeRecordLimits) ToOneof() OneofDeviceFeature {
	return p
}

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

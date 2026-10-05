package entity

type OneofStorageMetadata interface {
	isOneofStorageMetadata()
}

func (x *LtfsMetadata) isOneofStorageMetadata() {}

func (x *StorageMetadata) Unpack() OneofStorageMetadata {
	switch param := x.Backend.(type) {
	case *StorageMetadata_Ltfs:
		return param.Ltfs
	default:
		return nil
	}
}

func PackStorageMetadata(param OneofStorageMetadata) *StorageMetadata {
	switch param := param.(type) {
	case *LtfsMetadata:
		return &StorageMetadata{
			Backend: &StorageMetadata_Ltfs{
				Ltfs: param,
			},
		}
	default:
		return nil
	}
}

func (p *LtfsMetadata) Pack() *StorageMetadata {
	return &StorageMetadata{
		Backend: &StorageMetadata_Ltfs{
			Ltfs: p,
		},
	}
}

func (p *LtfsMetadata) ToOneof() OneofStorageMetadata {
	return p
}

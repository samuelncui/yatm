package entity

type OneofStorageMetadata interface {
	isOneofStorageMetadata()
}

func (x *LtfsMetadata) isOneofStorageMetadata() {}

func (x *TarMetadata) isOneofStorageMetadata() {}

func (x *StorageMetadata) Unpack() OneofStorageMetadata {
	switch param := x.Backend.(type) {
	case *StorageMetadata_Ltfs:
		return param.Ltfs
	case *StorageMetadata_Tar:
		return param.Tar
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
	case *TarMetadata:
		return &StorageMetadata{
			Backend: &StorageMetadata_Tar{
				Tar: param,
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

func (p *TarMetadata) Pack() *StorageMetadata {
	return &StorageMetadata{
		Backend: &StorageMetadata_Tar{
			Tar: p,
		},
	}
}

func (p *TarMetadata) ToOneof() OneofStorageMetadata {
	return p
}

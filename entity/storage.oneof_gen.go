package entity

type OneofStorageMetadata interface {
	isOneofStorageMetadata()
}

func (x *LTFSMetadata) isOneofStorageMetadata() {}

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
	case *LTFSMetadata:
		return &StorageMetadata{
			Backend: &StorageMetadata_Ltfs{
				Ltfs: param,
			},
		}
	default:
		return nil
	}
}

func (p *LTFSMetadata) Pack() *StorageMetadata {
	return &StorageMetadata{
		Backend: &StorageMetadata_Ltfs{
			Ltfs: p,
		},
	}
}

func (p *LTFSMetadata) ToOneof() OneofStorageMetadata {
	return p
}

package entity

type OneofFileSelection interface {
	isOneofFileSelection()
}

func (x *LibrarySelection) isOneofFileSelection() {}

func (x *LocationSelection) isOneofFileSelection() {}

func (x *FileSelection) Unpack() OneofFileSelection {
	switch param := x.Target.(type) {
	case *FileSelection_Library:
		return param.Library
	case *FileSelection_Location:
		return param.Location
	default:
		return nil
	}
}

func PackFileSelection(param OneofFileSelection) *FileSelection {
	switch param := param.(type) {
	case *LibrarySelection:
		return &FileSelection{
			Target: &FileSelection_Library{
				Library: param,
			},
		}
	case *LocationSelection:
		return &FileSelection{
			Target: &FileSelection_Location{
				Location: param,
			},
		}
	default:
		return nil
	}
}

func (p *LibrarySelection) Pack() *FileSelection {
	return &FileSelection{
		Target: &FileSelection_Library{
			Library: p,
		},
	}
}

func (p *LibrarySelection) ToOneof() OneofFileSelection {
	return p
}

func (p *LocationSelection) Pack() *FileSelection {
	return &FileSelection{
		Target: &FileSelection_Location{
			Location: p,
		},
	}
}

func (p *LocationSelection) ToOneof() OneofFileSelection {
	return p
}

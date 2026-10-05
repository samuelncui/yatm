package entity

type OneofPreviewGeneratorSettings interface {
	isOneofPreviewGeneratorSettings()
}

func (x *ImagePreviewSettings) isOneofPreviewGeneratorSettings() {}

func (x *VideoPreviewSettings) isOneofPreviewGeneratorSettings() {}

func (x *PreviewGeneratorSettings) Unpack() OneofPreviewGeneratorSettings {
	switch param := x.Options.(type) {
	case *PreviewGeneratorSettings_Image:
		return param.Image
	case *PreviewGeneratorSettings_Video:
		return param.Video
	default:
		return nil
	}
}

func PackPreviewGeneratorSettings(param OneofPreviewGeneratorSettings) *PreviewGeneratorSettings {
	switch param := param.(type) {
	case *ImagePreviewSettings:
		return &PreviewGeneratorSettings{
			Options: &PreviewGeneratorSettings_Image{
				Image: param,
			},
		}
	case *VideoPreviewSettings:
		return &PreviewGeneratorSettings{
			Options: &PreviewGeneratorSettings_Video{
				Video: param,
			},
		}
	default:
		return nil
	}
}

func (p *ImagePreviewSettings) Pack() *PreviewGeneratorSettings {
	return &PreviewGeneratorSettings{
		Options: &PreviewGeneratorSettings_Image{
			Image: p,
		},
	}
}

func (p *ImagePreviewSettings) ToOneof() OneofPreviewGeneratorSettings {
	return p
}

func (p *VideoPreviewSettings) Pack() *PreviewGeneratorSettings {
	return &PreviewGeneratorSettings{
		Options: &PreviewGeneratorSettings_Video{
			Video: p,
		},
	}
}

func (p *VideoPreviewSettings) ToOneof() OneofPreviewGeneratorSettings {
	return p
}

type OneofSettingsValue interface {
	isOneofSettingsValue()
}

func (x *JobSettings) isOneofSettingsValue() {}

func (x *LibrarySettings) isOneofSettingsValue() {}

func (x *PreviewSettings) isOneofSettingsValue() {}

func (x *SettingsValue) Unpack() OneofSettingsValue {
	switch param := x.Value.(type) {
	case *SettingsValue_Job:
		return param.Job
	case *SettingsValue_Library:
		return param.Library
	case *SettingsValue_Preview:
		return param.Preview
	default:
		return nil
	}
}

func PackSettingsValue(param OneofSettingsValue) *SettingsValue {
	switch param := param.(type) {
	case *JobSettings:
		return &SettingsValue{
			Value: &SettingsValue_Job{
				Job: param,
			},
		}
	case *LibrarySettings:
		return &SettingsValue{
			Value: &SettingsValue_Library{
				Library: param,
			},
		}
	case *PreviewSettings:
		return &SettingsValue{
			Value: &SettingsValue_Preview{
				Preview: param,
			},
		}
	default:
		return nil
	}
}

func (p *JobSettings) Pack() *SettingsValue {
	return &SettingsValue{
		Value: &SettingsValue_Job{
			Job: p,
		},
	}
}

func (p *JobSettings) ToOneof() OneofSettingsValue {
	return p
}

func (p *LibrarySettings) Pack() *SettingsValue {
	return &SettingsValue{
		Value: &SettingsValue_Library{
			Library: p,
		},
	}
}

func (p *LibrarySettings) ToOneof() OneofSettingsValue {
	return p
}

func (p *PreviewSettings) Pack() *SettingsValue {
	return &SettingsValue{
		Value: &SettingsValue_Preview{
			Preview: p,
		},
	}
}

func (p *PreviewSettings) ToOneof() OneofSettingsValue {
	return p
}

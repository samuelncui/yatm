package entity

type OneofArchiveMediaTarget interface {
	isOneofArchiveMediaTarget()
}

func (x *ArchiveTapeTarget) isOneofArchiveMediaTarget() {}

func (x *ArchiveVolumeTarget) isOneofArchiveMediaTarget() {}

func (x *ArchiveMediaTarget) Unpack() OneofArchiveMediaTarget {
	switch param := x.Backend.(type) {
	case *ArchiveMediaTarget_Tape:
		return param.Tape
	case *ArchiveMediaTarget_Volume:
		return param.Volume
	default:
		return nil
	}
}

func PackArchiveMediaTarget(param OneofArchiveMediaTarget) *ArchiveMediaTarget {
	switch param := param.(type) {
	case *ArchiveTapeTarget:
		return &ArchiveMediaTarget{
			Backend: &ArchiveMediaTarget_Tape{
				Tape: param,
			},
		}
	case *ArchiveVolumeTarget:
		return &ArchiveMediaTarget{
			Backend: &ArchiveMediaTarget_Volume{
				Volume: param,
			},
		}
	default:
		return nil
	}
}

func (p *ArchiveTapeTarget) Pack() *ArchiveMediaTarget {
	return &ArchiveMediaTarget{
		Backend: &ArchiveMediaTarget_Tape{
			Tape: p,
		},
	}
}

func (p *ArchiveTapeTarget) ToOneof() OneofArchiveMediaTarget {
	return p
}

func (p *ArchiveVolumeTarget) Pack() *ArchiveMediaTarget {
	return &ArchiveMediaTarget{
		Backend: &ArchiveMediaTarget_Volume{
			Volume: p,
		},
	}
}

func (p *ArchiveVolumeTarget) ToOneof() OneofArchiveMediaTarget {
	return p
}

type OneofMediaProfile interface {
	isOneofMediaProfile()
}

func (x *TapeMediaProfile) isOneofMediaProfile() {}

func (x *VolumeMediaProfile) isOneofMediaProfile() {}

func (x *MediaProfile) Unpack() OneofMediaProfile {
	switch param := x.Kind.(type) {
	case *MediaProfile_Tape:
		return param.Tape
	case *MediaProfile_Volume:
		return param.Volume
	default:
		return nil
	}
}

func PackMediaProfile(param OneofMediaProfile) *MediaProfile {
	switch param := param.(type) {
	case *TapeMediaProfile:
		return &MediaProfile{
			Kind: &MediaProfile_Tape{
				Tape: param,
			},
		}
	case *VolumeMediaProfile:
		return &MediaProfile{
			Kind: &MediaProfile_Volume{
				Volume: param,
			},
		}
	default:
		return nil
	}
}

func (p *TapeMediaProfile) Pack() *MediaProfile {
	return &MediaProfile{
		Kind: &MediaProfile_Tape{
			Tape: p,
		},
	}
}

func (p *TapeMediaProfile) ToOneof() OneofMediaProfile {
	return p
}

func (p *VolumeMediaProfile) Pack() *MediaProfile {
	return &MediaProfile{
		Kind: &MediaProfile_Volume{
			Volume: p,
		},
	}
}

func (p *VolumeMediaProfile) ToOneof() OneofMediaProfile {
	return p
}

type OneofReadMediaTarget interface {
	isOneofReadMediaTarget()
}

func (x *ReadTapeTarget) isOneofReadMediaTarget() {}

func (x *ReadVolumeTarget) isOneofReadMediaTarget() {}

func (x *ReadMediaTarget) Unpack() OneofReadMediaTarget {
	switch param := x.Backend.(type) {
	case *ReadMediaTarget_Tape:
		return param.Tape
	case *ReadMediaTarget_Volume:
		return param.Volume
	default:
		return nil
	}
}

func PackReadMediaTarget(param OneofReadMediaTarget) *ReadMediaTarget {
	switch param := param.(type) {
	case *ReadTapeTarget:
		return &ReadMediaTarget{
			Backend: &ReadMediaTarget_Tape{
				Tape: param,
			},
		}
	case *ReadVolumeTarget:
		return &ReadMediaTarget{
			Backend: &ReadMediaTarget_Volume{
				Volume: param,
			},
		}
	default:
		return nil
	}
}

func (p *ReadTapeTarget) Pack() *ReadMediaTarget {
	return &ReadMediaTarget{
		Backend: &ReadMediaTarget_Tape{
			Tape: p,
		},
	}
}

func (p *ReadTapeTarget) ToOneof() OneofReadMediaTarget {
	return p
}

func (p *ReadVolumeTarget) Pack() *ReadMediaTarget {
	return &ReadMediaTarget{
		Backend: &ReadMediaTarget_Volume{
			Volume: p,
		},
	}
}

func (p *ReadVolumeTarget) ToOneof() OneofReadMediaTarget {
	return p
}

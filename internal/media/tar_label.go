package media

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/tape"
	"google.golang.org/protobuf/encoding/protojson"
)

// TarLabel identifies file zero without storing an encryption key or a content index.
type TarLabel struct {
	Format  string
	Barcode string
	Name    string
	Profile *entity.TarMediaProfile
}

type tarLabelJSON struct {
	Format  string          `json:"format"`
	Barcode string          `json:"barcode"`
	Name    string          `json:"name"`
	Profile json.RawMessage `json:"profile"`
}

const tarLabelLimit = 64 << 10

// WriteTarLabel emits an ordinary PAX identity archive at the prepared BOT cursor.
// It writes no filemark; the preparation owner must qualify physical completion afterward.
func WriteTarLabel(ctx context.Context, device TarRecordDevice, mode tape.RecordMode, label *TarLabel) error {
	// Keep the independent recovery descriptor small and exclude all key-bearing Tape fields.
	if label == nil || label.Profile == nil {
		return errors.New("tar identity requires a profile")
	}
	if err := entity.ValidateTarProfile(label.Profile); err != nil {
		return err
	}
	if int(label.Profile.RecordBytes) != mode.MaximumBytes ||
		(label.Profile.RecordMode == entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED) != (mode.FixedBytes != 0) {
		return errors.New("tar label record policy differs from the prepared transport")
	}
	profile, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(label.Profile)
	if err != nil {
		return err
	}
	identity, err := json.Marshal(tarLabelJSON{
		Format: label.Format, Barcode: label.Barcode, Name: label.Name, Profile: profile,
	})
	if err != nil {
		return err
	}
	if len(identity) > tarLabelLimit {
		return errors.New("tar identity exceeds supported envelope")
	}
	readme := []byte(fmt.Sprintf("YATM %s Tape\nBarcode: %s\nName: %q\n"+
		"Physical Tape file 0 contains this identity. Each later Tape file is an independent PAX tar.\n"+
		"Recovery requires the recorded record policy and any required hardware encryption key.\n"+
		"Record mode: %s; record bytes: %d; hardware compression: %t.\n"+
		"Hardware-compressed records require a compatible drive decoder; read DDE is independent of write DCE.\n"+
		"Observed encryption algorithm: %s (empty means not recorded). Any key is retained outside this label.\n"+
		"For encrypted recovery, obtain the key from the saved Media profile and use a temporary protected key file.\n"+
		"With stenc 2.0.1, resolve this reader's public algorithm index; after positioning, use "+
		"-e off -d on -k KEY_FILE -a READER_INDEX, then query and verify the effective read state.\n"+
		"After copying, use -e off -d off -a READER_INDEX and verify key release. Closing alone does not clear it.\n"+
		"On Linux, release all YATM/LTFS device ownership and write-protect the recovery cartridge.\n"+
		"Use a compatible non-rewinding st device. Set fixed mode with mt setblk record_bytes, "+
		"or variable mode with mt setblk 0; check each command's exit status.\n"+
		"For Tape file N, rewind, then mt fsf N from BOT each time (N=0 is this label).\n"+
		"Copy one Tape file to a new archive-N.tar using GNU dd bs=record_bytes conv=excl. "+
		"The variable-mode buffer must cover every physical record.\n"+
		"Keep dd's exit status and mt status. A read error is an incomplete copy; "+
		"never use read-error padding, conv=noerror, conv=sync or iflag=fullblock.\n"+
		"Extract the disk copy into a fresh directory with GNU tar --xattrs --same-permissions "+
		"--no-same-owner --extract --file=archive-N.tar --directory=output-N.\n"+
		"A truncated tail may report an error and leave partial last output; retain complete earlier "+
		"members and discard the incomplete member. Header checksums do not verify payload hashes.\n"+
		"An empty Tape file is not proof of recorded EOD. Qualify the reader's EOF/EOD/EOT status and "+
		"close behavior before automatically continuing to another Tape file.\n"+
		"Other xattr namespaces require explicit compatible-system GNU tar selection. "+
		"No YATM database or index is needed for content extraction.\n",
		label.Format, label.Barcode, label.Name, label.Profile.RecordMode.String(),
		label.Profile.RecordBytes, label.Profile.HardwareCompression, label.Profile.GetEncryptionAlgorithm()))
	if len(readme) > tarLabelLimit {
		return errors.New("tar identity README exceeds supported envelope")
	}

	// Use the same record packer and standard serializer as content archives.
	options := TarSessionOptions{Media: &Descriptor{}, Mode: mode, Cleanup: func(context.Context) error { return nil }}
	if err := checkTarOptions(device, options); err != nil {
		return err
	}
	s := &tarWriteSession{device: device, options: options, ctx: ctx, buffer: make([]byte, 0, mode.MaximumBytes)}
	writer := tar.NewWriter(s)
	for _, member := range []struct {
		name string
		data []byte
	}{{"YATM/identity.json", identity}, {"YATM/README.txt", readme}} {
		if err := writer.WriteHeader(&tar.Header{
			Name: member.name, Typeflag: tar.TypeReg, Format: tar.FormatPAX,
			Size: int64(len(member.data)), Mode: 0644, ModTime: time.Unix(0, 0),
		}); err != nil {
			return err
		}
		if _, err := writer.Write(member.data); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if len(s.buffer) != 0 {
		if mode.FixedBytes != 0 {
			s.buffer = append(s.buffer, make([]byte, cap(s.buffer)-len(s.buffer))...)
		}
		if err := s.writeRecord(); err != nil {
			return err
		}
	}
	return nil
}

// ReadTarLabel reads a bounded identity archive at the prepared BOT cursor through its filemark.
// The owner validates the returned identity/profile against its operation and cleans up on failure.
func ReadTarLabel(ctx context.Context, device TarRecordDevice, mode tape.RecordMode) (*TarLabel, error) {
	// Bound both identity members before allocating their contents.
	options := TarSessionOptions{Media: &Descriptor{}, Mode: mode, Cleanup: func(context.Context) error { return nil }}
	if err := checkTarOptions(device, options); err != nil {
		return nil, err
	}
	r := &tarRecordReader{device: device, ctx: ctx, buffer: make([]byte, mode.MaximumBytes)}
	var identity []byte
	for _, name := range []string{"YATM/identity.json", "YATM/README.txt"} {
		member, err := nextTarMember(r, 0)
		if err != nil {
			return nil, err
		}
		if member.header.Name != name || member.header.Size > tarLabelLimit {
			return nil, errors.New("tar identity members are invalid or oversized")
		}
		data, err := io.ReadAll(member.reader)
		if err != nil {
			return nil, err
		}
		if err := finishTarMember(r, member); err != nil {
			return nil, err
		}
		if name == "YATM/identity.json" {
			identity = data
		}
	}

	// A valid identity archive has a standard terminator and a proven physical filemark.
	if _, err := nextTarMember(r, 0); !errors.Is(err, io.EOF) || r.boundary.Filemark || r.boundary.EOD {
		return nil, errors.New("tar identity archive lacks its complete terminator")
	}
	s := &tarReadSession{records: r}
	if err := s.consumeTrailer(); err != nil {
		return nil, err
	}
	if !r.boundary.Filemark {
		return nil, errors.New("tar identity archive lacks a terminating filemark")
	}
	var document tarLabelJSON
	if err := json.Unmarshal(identity, &document); err != nil {
		return nil, fmt.Errorf("decode tar identity failed, %w", err)
	}
	profile := &entity.TarMediaProfile{}
	if err := protojson.Unmarshal(document.Profile, profile); err != nil {
		return nil, fmt.Errorf("decode tar identity profile failed, %w", err)
	}
	if err := entity.ValidateTarProfile(profile); err != nil {
		return nil, err
	}
	return &TarLabel{Format: document.Format, Barcode: document.Barcode, Name: document.Name, Profile: profile}, nil
}

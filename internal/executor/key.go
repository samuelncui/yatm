package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/internal/resource"
	"github.com/sirupsen/logrus"
)

const (
	keySize     = 256
	keyV1Header = "v1:"
)

// restoreKey returns (path, recycle, error)
func (e *Executor) RestoreKey(str string) (_ string, _ func(), rerr error) {
	// Hold the disposable key directory through the physical operation's recycle callback.
	temporary, err := resource.NewTemporaryDirectory(e.paths.Work, resource.TemporaryKeyPrefix)
	if err != nil {
		return "", nil, fmt.Errorf("restore key, create temporary directory, %w", err)
	}
	defer func() {
		if rerr != nil {
			rerr = errors.Join(rerr, temporary.Close())
		}
	}()
	path := filepath.Join(temporary.Directory, "key")
	value := ""
	if strings.HasPrefix(str, keyV1Header) {
		value = str[len(keyV1Header):]
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		return "", nil, fmt.Errorf("restore key, write key, %w", err)
	}

	// Returning the key transfers its immediate cleanup to the admitted Media operation.
	return path, func() {
		if err := temporary.Close(); err != nil {
			logrus.WithError(err).Warn("remove temporary Tape key failed")
		}
	}, nil
}

// newKey returns (key, path, recycle, error)
func (e *Executor) NewKey() (string, string, func(), error) {
	// Both raw tar and the existing LTFS adapter use the same encoded 256-bit key representation.
	key, err := newTapeKey()
	if err != nil {
		return "", "", nil, err
	}
	path, recycle, err := e.RestoreKey(key)
	if err != nil {
		return "", "", nil, err
	}
	return key, path, recycle, nil
}

func newTapeKey() (string, error) {
	keyBuf := make([]byte, keySize/8)
	defer clear(keyBuf)
	if _, err := rand.Reader.Read(keyBuf); err != nil {
		return "", fmt.Errorf("gen key fail, %w", err)
	}
	return keyV1Header + hex.EncodeToString(keyBuf), nil
}

func decodeTarKey(encoded string) ([]byte, error) {
	// Raw tar accepts the retained key representation without weakening the legacy LTFS adapter.
	if !strings.HasPrefix(encoded, keyV1Header) || len(encoded) != len(keyV1Header)+keySize/4 {
		return nil, fmt.Errorf("tar encryption key requires the v1 256-bit hexadecimal representation")
	}
	key, err := hex.DecodeString(encoded[len(keyV1Header):])
	if err != nil {
		clear(key)
		return nil, fmt.Errorf("tar encryption key has invalid hexadecimal encoding")
	}
	return key, nil
}

func (e *Executor) MakeEncryptCmd(ctx context.Context, device, keyPath, barcode, name string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, e.scripts.Encrypt)
	cmd.Env = append(cmd.Env, fmt.Sprintf("DEVICE=%s", device), fmt.Sprintf("KEY_FILE=%s", keyPath), fmt.Sprintf("TAPE_BARCODE=%s", barcode), fmt.Sprintf("TAPE_NAME=%s", name))
	return cmd
}

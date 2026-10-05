package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/sirupsen/logrus"
)

const (
	keySize     = 256
	keyV1Header = "v1:"
)

// restoreKey returns (path, recycle, error)
func (e *Executor) RestoreKey(str string) (_ string, _ func(), rerr error) {
	// Retain cleanup ownership until the fully written, closed key reaches the caller.
	file, err := os.CreateTemp("", "*.key")
	if err != nil {
		return "", nil, fmt.Errorf("restore key, create temp, %w", err)
	}
	defer func() {
		rerr = errors.Join(rerr, file.Close())
		if rerr != nil {
			rerr = errors.Join(rerr, os.Remove(file.Name()))
		}
	}()

	if strings.HasPrefix(str, keyV1Header) {
		if _, err := file.WriteString(str[len(keyV1Header):]); err != nil {
			return "", nil, fmt.Errorf("restore key, write key, %w", err)
		}
	}

	return file.Name(), func() {
		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			logrus.WithError(err).Warn("remove temporary Tape key failed")
		}
	}, nil
}

// newKey returns (key, path, recycle, error)
func (e *Executor) NewKey() (string, string, func(), error) {
	keyBuf := make([]byte, keySize/8)
	if _, err := rand.Reader.Read(keyBuf); err != nil {
		return "", "", nil, fmt.Errorf("gen key fail, %w", err)
	}
	key := keyV1Header + hex.EncodeToString(keyBuf)

	path, recycle, err := e.RestoreKey(key)
	if err != nil {
		return "", "", nil, err
	}

	return key, path, recycle, nil
}

func (e *Executor) MakeEncryptCmd(ctx context.Context, device, keyPath, barcode, name string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, e.scripts.Encrypt)
	cmd.Env = append(cmd.Env, fmt.Sprintf("DEVICE=%s", device), fmt.Sprintf("KEY_FILE=%s", keyPath), fmt.Sprintf("TAPE_BARCODE=%s", barcode), fmt.Sprintf("TAPE_NAME=%s", name))
	return cmd
}

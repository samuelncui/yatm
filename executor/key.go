package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	keySize     = 256
	keyV1Header = "v1:"
)

// restoreKey returns (path, recycle, error)
func (e *Executor) restoreKey(str string) (string, func(), error) {
	file, err := os.CreateTemp("", "*.key")
	if err != nil {
		return "", nil, fmt.Errorf("restore key, create temp, %w", err)
	}
	defer file.Close()

	if strings.HasPrefix(str, keyV1Header) {
		if _, err := file.WriteString(str[len(keyV1Header):]); err != nil {
			return "", nil, fmt.Errorf("restore key, write key, %w", err)
		}
	}

	return file.Name(), func() { os.Remove(file.Name()) }, nil
}

// newKey returns (key, path, recycle, error)
func (e *Executor) newKey() (string, string, func(), error) {
	keyBuf := make([]byte, keySize/8)
	if _, err := rand.Reader.Read(keyBuf); err != nil {
		return "", "", nil, fmt.Errorf("gen key fail, %w", err)
	}
	key := keyV1Header + hex.EncodeToString(keyBuf)

	path, recycle, err := e.restoreKey(key)
	if err != nil {
		return "", "", nil, err
	}
	return key, path, recycle, nil
}

func (e *Executor) makeEncryptCmd(ctx context.Context, device, keyPath, barcode, name string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, e.encryptScript)
	cmd.Env = append(cmd.Env, fmt.Sprintf("DEVICE=%s", device), fmt.Sprintf("KEY_FILE=%s", keyPath), fmt.Sprintf("TAPE_BARCODE=%s", barcode), fmt.Sprintf("TAPE_NAME=%s", name))
	return cmd
}

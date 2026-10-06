package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/samuelncui/yatm/internal/tools"
	"github.com/sirupsen/logrus"
)

// ReadTapeBarcode returns an empty value when the probe explicitly reports no barcode.
// FORMAT writes the requested barcode; APPEND and Restore require an existing identity.
func (e *Executor) ReadTapeBarcode(ctx context.Context, device string) (string, error) {
	// Run the configured device probe and decode its stable JSON boundary.
	cmd := exec.CommandContext(ctx, e.scripts.ReadInfo)
	cmd.Env = append(cmd.Env, fmt.Sprintf("DEVICE=%s", device))
	data, err := tools.RunCmdWithReturn(logrus.StandardLogger(), cmd)
	if err != nil {
		return "", fmt.Errorf("read tape info failed, %w", err)
	}
	var info struct {
		Barcode *string `json:"barcode"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return "", fmt.Errorf("decode tape info failed, %w", err)
	}
	if info.Barcode == nil {
		return "", fmt.Errorf("read tape info failed: missing barcode field")
	}
	if strings.TrimSpace(*info.Barcode) == "" {
		return "", nil
	}
	// Historical probes return the cartridge label with its media suffix (for example ABC001L5).
	// Keep their six-character identity normalization at this boundary, not on user-supplied targets.
	value := strings.TrimSpace(*info.Barcode)
	if len(value) > 6 {
		value = value[:6]
	}
	barcode, err := NormalizeTapeBarcode(value)
	if err != nil {
		return "", fmt.Errorf("invalid tape barcode, %w", err)
	}
	return barcode, nil
}

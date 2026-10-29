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
	data, err := tools.RunCmdWithReturn(logrus.StandardLogger(), cmd, e.paths.Work)
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
	// Keep reported cartridge labels separate from strict operator-supplied targets.
	return normalizeReportedTapeBarcode(*info.Barcode)
}

func normalizeReportedTapeBarcode(value string) (string, error) {
	// Historical probes and MAM can include the media suffix after the six-character identity.
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > 6 {
		value = value[:6]
	}
	barcode, err := NormalizeTapeBarcode(value)
	if err != nil {
		return "", fmt.Errorf("invalid tape barcode, %w", err)
	}
	return barcode, nil
}

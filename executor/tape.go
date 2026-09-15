package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/samuelncui/yatm/tools"
	"github.com/sirupsen/logrus"
)

// ReadTapeBarcode returns an empty value when the probe explicitly reports no barcode.
// Read/write Sessions reject that result; read-only inspection may use its documented fallback.
func (e *Executor) ReadTapeBarcode(ctx context.Context, device string) (string, error) {
	// Run the configured device probe and decode its stable JSON boundary.
	cmd := exec.CommandContext(ctx, e.scripts.ReadInfo)
	cmd.Env = append(cmd.Env, fmt.Sprintf("DEVICE=%s", device))
	data, err := tools.RunCmdWithReturn(logrus.StandardLogger(), cmd)
	if err != nil {
		return "", fmt.Errorf("read tape info failed, %w", err)
	}
	var info struct {
		Barcode string `json:"barcode"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return "", fmt.Errorf("decode tape info failed, %w", err)
	}
	if strings.TrimSpace(info.Barcode) == "" {
		return "", nil
	}
	barcode, err := NormalizeTapeBarcode(info.Barcode)
	if err != nil {
		return "", fmt.Errorf("invalid tape barcode, %w", err)
	}
	return barcode, nil
}

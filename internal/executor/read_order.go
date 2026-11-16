package executor

import (
	"context"
	"errors"
	"iter"
	"strings"
	"time"

	"github.com/samuelncui/yatm/entity"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/sirupsen/logrus"
)

// PlanReadOrder plans one attempt's complete read route under the shared Work root, reports the
// effective order to the Session and logs it once. An expired planning budget returns a nil
// Sequence so the caller keeps its LOGICAL manifest order.
func PlanReadOrder(
	ctx context.Context,
	logger *logrus.Logger,
	operation string,
	root string,
	ordered mediapkg.OrderedReadSession,
	provider mediapkg.ReadOrderProvider,
	spans iter.Seq2[readplan.Span, error],
) (*readplan.Sequence, error) {
	started := time.Now()
	sequence, err := readplan.Plan(ctx, root, provider, spans)
	elapsed := time.Since(started).Nanoseconds()
	order, reason := entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL, "LOGICAL: read planning budget expired"
	fields := map[string]any{}
	switch {
	case errors.Is(err, readplan.ErrBudget):
		sequence = nil
	case err != nil:
		return nil, err
	default:
		order, reason = sequence.Order(), sequence.Reason()
		if logical, planned, known := sequence.Estimates(); known {
			fields["estimated_logical_ms"], fields["estimated_planned_ms"] = logical.Milliseconds(), planned.Milliseconds()
		}
	}

	// Explain the selected strategy once without mixing planned ranks with physical observations;
	// an inconsistent planner estimate keeps LOGICAL and is reported as a warning.
	ordered.ReadOrderPlanned(order, reason)
	if logger != nil {
		entry := logger.WithContext(ctx).WithFields(fields).WithField("read_order", reason).WithField("planning_ns", elapsed)
		if strings.HasPrefix(reason, readplan.ReasonInconsistent) {
			entry.Warn(operation + " read order prepared")
		} else {
			entry.Info(operation + " read order prepared")
		}
	}
	return sequence, nil
}

// LogReadOrderRealized logs a planned attempt's realized read time beside its estimates so a run can
// be compared with its prediction. elapsed runs from the end of planning until every read settled;
// the estimates also include the terminal action, which follows later.
func LogReadOrderRealized(ctx context.Context, logger *logrus.Logger, operation string, sequence *readplan.Sequence,
	elapsed time.Duration,
) {
	if logger == nil || sequence == nil {
		return
	}
	logical, planned, known := sequence.Estimates()
	if !known {
		return
	}
	logger.WithContext(ctx).WithField("read_order", sequence.Reason()).
		WithField("estimated_logical_ms", logical.Milliseconds()).WithField("estimated_planned_ms", planned.Milliseconds()).
		WithField("realized_ms", elapsed.Milliseconds()).Info(operation + " read order completed")
}

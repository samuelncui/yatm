package executor

import (
	"context"
	"errors"
	"iter"
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
	switch {
	case errors.Is(err, readplan.ErrBudget):
		sequence = nil
	case err != nil:
		return nil, err
	default:
		order, reason = sequence.Order(), sequence.Reason()
	}

	// Explain the selected strategy once without mixing planned ranks with physical observations.
	ordered.ReadOrderPlanned(order, reason)
	if logger != nil {
		logger.WithContext(ctx).WithField("read_order", reason).
			WithField("planning_ns", elapsed).Info(operation + " read order prepared")
	}
	return sequence, nil
}

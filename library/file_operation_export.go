package library

import (
	"context"
	"encoding/json"
)

func (l *Library) exportFileOperationResults(ctx context.Context, encoder *json.Encoder) error {
	// The compound business key provides a stable, bounded export cursor.
	operation, item := "", int64(0)
	for {
		var rows []*FileOperationResult
		err := l.db.WithContext(ctx).Where("operation_id > ? OR (operation_id = ? AND item_id > ?)", operation, operation, item).
			Order("operation_id, item_id").Limit(batchSize).Find(&rows).Error
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if err := encoder.Encode(jsonlOutputRecord{Type: recordTypeFileOperationResult, Data: row}); err != nil {
				return err
			}
			operation, item = row.OperationID, row.ItemID
		}
	}
}

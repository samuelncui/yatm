package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/samuelncui/yatm/entity"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// tapeReadObservationsFile is the JSONL file of passive read learning observations under a Job bundle's
// tapes/<barcode>/ directory. It shares the bundle's lifetime; deleting the Job deletes it.
const tapeReadObservationsFile = "read-observations.jsonl"

// observationAction names the native Tape action one observation records.
type observationAction string

const (
	observationLocate observationAction = "LOCATE"
	observationRewind observationAction = "REWIND"
	// observationRead is one READ segment: contiguous data READs between other commands.
	observationRead      observationAction = "READ"
	observationFirstRead observationAction = "FIRST_READ"
	// observationFilemarkRead is a READ that crossed a filemark plus the following data READ.
	observationFilemarkRead observationAction = "FILEMARK_READ"
	observationUnload       observationAction = "UNLOAD"
)

// observationPoint is one endpoint's logical address and, when the position map resolved it,
// the physical wrap and longitudinal Offset within Span logical objects from the wrap's BOT-side end.
type observationPoint struct {
	Partition *uint32 `json:"partition,omitempty"`
	Object    *uint64 `json:"object,omitempty"`
	File      *uint64 `json:"file,omitempty"`
	Wrap      *uint32 `json:"wrap,omitempty"`
	Offset    *uint64 `json:"offset,omitempty"`
	Span      *uint64 `json:"span,omitempty"`
}

// tapeReadObservation is one completed native Tape action: raw facts only, never derived features.
type tapeReadObservation struct {
	Device string `json:"device"`
	// Scope is the protobuf JSON of the learned read parameters' TapeReadApplicability.
	Scope   json.RawMessage `json:"scope"`
	JobID   int64           `json:"job_id"`
	Barcode string          `json:"barcode"`

	// Position map that resolved the physical points, and its LPOS frame when it has one.
	MapSource       string  `json:"map_source,omitempty"`
	MapObservedAtNS *int64  `json:"map_observed_at_ns,omitempty,string"`
	MinLPOS         *uint64 `json:"min_lpos,omitempty"`
	MaxLPOS         *uint64 `json:"max_lpos,omitempty"`

	Opcode uint8             `json:"opcode"`
	Action observationAction `json:"action"`
	// PreviousAction is empty for another command or an unknown predecessor.
	PreviousAction observationAction `json:"previous_action,omitempty"`
	StartedAtNS    int64             `json:"started_at_ns,string"`
	// IdleSeconds is the host gap before this action; absent after a sequence gap or at collection start.
	IdleSeconds *float64         `json:"idle_seconds,omitempty"`
	From        observationPoint `json:"from"`
	To          observationPoint `json:"to"`

	Commands          uint32  `json:"commands"`
	Bytes             uint64  `json:"bytes"`
	DeviceSeconds     float64 `json:"device_seconds"`
	HostSeconds       float64 `json:"host_seconds"` // Host gaps between this action's commands.
	MaxHostGapSeconds float64 `json:"max_host_gap_seconds"`
	Discard           bool    `json:"discard"`
}

// appendTapeReadObservations adds one attempt's observations to the Job bundle's file for this cartridge.
func (e *Executor) appendTapeReadObservations(ctx context.Context, jobID int64, barcode string,
	values []tapeReadObservation,
) (returnErr error) {
	dir, err := e.EnsureTapeWorkPath(ctx, jobID, barcode)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, tapeReadObservationsFile), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open Tape read observations failed, %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close Tape read observations failed, %w", err))
		}
	}()
	writer := bufio.NewWriter(file)
	encoder := json.NewEncoder(writer)
	for i := range values {
		if err := encoder.Encode(&values[i]); err != nil {
			return fmt.Errorf("write Tape read observations failed, %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("write Tape read observations failed, %w", err)
	}
	return nil
}

// visitTapeReadObservations streams the observations of every existing Job for one device and
// applicability, in Job order. An unreadable or truncated file is skipped from its first bad line.
func (e *Executor) visitTapeReadObservations(ctx context.Context, logger *logrus.Logger, device string,
	scope *entity.TapeReadApplicability, visit func(*tapeReadObservation),
) error {
	// The Job catalog names the existing bundles; deleted Jobs and their files are gone.
	var ids []int64
	if err := e.readDB().WithContext(ctx).Model(&jobCatalogRow{}).Order("id").Pluck("id", &ids).Error; err != nil {
		return fmt.Errorf("list Jobs for Tape read observations failed, %w", err)
	}
	scopes := make(map[string]bool)
	for _, id := range ids {
		paths, err := filepath.Glob(filepath.Join(e.jobWorkPath(id), "tapes", "*", tapeReadObservationsFile))
		if err != nil {
			return err
		}
		for _, path := range paths {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := readTapeReadObservations(path, device, scope, scopes, visit); err != nil {
				logger.WithContext(ctx).WithError(err).WithField("path", path).Warn("skip unreadable Tape read observations")
			}
		}
	}
	return nil
}

func readTapeReadObservations(path, device string, scope *entity.TapeReadApplicability, scopes map[string]bool,
	visit func(*tapeReadObservation),
) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // The Job was deleted after it was listed.
	}
	if err != nil {
		return err
	}
	defer file.Close()

	// Decode one line at a time; each distinct scope encoding is compared once.
	decoder := json.NewDecoder(bufio.NewReader(file))
	for {
		var value tapeReadObservation
		if err := decoder.Decode(&value); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		if value.Device != device {
			continue
		}
		matches, known := scopes[string(value.Scope)]
		if !known {
			var stored entity.TapeReadApplicability
			matches = protojson.Unmarshal(value.Scope, &stored) == nil && proto.Equal(&stored, scope)
			scopes[string(value.Scope)] = matches
		}
		if matches {
			visit(&value)
		}
	}
}

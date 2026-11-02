package application

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type WorkerTimelineTurn struct {
	WorkerRunID          string        `json:"worker_run_id"`
	AgentTurnID          string        `json:"agent_turn_id"`
	DisplayStreamID      string        `json:"display_stream_id"`
	Status               string        `json:"status"`
	StartedAt            time.Time     `json:"started_at"`
	TerminalAt           *time.Time    `json:"terminal_at,omitempty"`
	Prompt               string        `json:"prompt"`
	CheckpointVersion    uint64        `json:"checkpoint_version"`
	CheckpointComplete   bool          `json:"checkpoint_complete"`
	BackfillStatus       string        `json:"backfill_status"`
	Parts                []DisplayPart `json:"parts"`
	LatestCheckpointedAt *time.Time    `json:"latest_checkpointed_at,omitempty"`
	ProjectionLagMillis  int64         `json:"projection_lag_millis,omitempty"`
}

type WorkerTimelinePage struct {
	Turns      []WorkerTimelineTurn `json:"turns"`
	NextCursor string               `json:"next_cursor,omitempty"`
	HasMore    bool                 `json:"has_more"`
}

type WorkerTimelineQuery struct {
	Viewer          Viewer
	WorkerSessionID string
	BeforeCursor    string
	Limit           int
}

type WorkerTimelineReader interface {
	ReadWorkerTimeline(context.Context, WorkerTimelineQuery) (WorkerTimelinePage, error)
}

func EncodeTimelineCursor(startedAt time.Time, agentTurnID string) string {
	value := strconv.FormatInt(startedAt.UTC().UnixNano(), 10) + "\x00" + agentTurnID
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func DecodeTimelineCursor(value string) (time.Time, string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("timeline cursor is invalid")
	}
	parts := strings.Split(string(decoded), "\x00")
	if len(parts) != 2 || !canonical(parts[1]) {
		return time.Time{}, "", fmt.Errorf("timeline cursor is invalid")
	}
	nanoseconds, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || nanoseconds <= 0 {
		return time.Time{}, "", fmt.Errorf("timeline cursor is invalid")
	}
	return time.Unix(0, nanoseconds).UTC(), parts[1], nil
}

func DecodeCheckpoint(raw json.RawMessage) (DisplayCheckpoint, error) {
	var checkpoint DisplayCheckpoint
	if len(raw) == 0 {
		return checkpoint, nil
	}
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		return DisplayCheckpoint{}, fmt.Errorf("decode DisplayCheckpoint: %w", err)
	}
	if err := ValidateDisplayCheckpoint(checkpoint); err != nil {
		return DisplayCheckpoint{}, err
	}
	return checkpoint, nil
}

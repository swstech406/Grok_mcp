package panel

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

const (
	defaultPanelPageLimit = 50
	maximumPanelPageLimit = 100
)

const (
	cursorKindKeys         = "keys"
	cursorKindUsers        = "users"
	cursorKindTiers        = "tiers"
	cursorKindInvites      = "invites"
	cursorKindUsageRecords = "usage_records"
)

type panelCursor struct {
	Kind      string `json:"kind"`
	Timestamp string `json:"timestamp,omitempty"`
	StringID  string `json:"string_id,omitempty"`
	NumericID int64  `json:"numeric_id,omitempty"`
	Level     int    `json:"level,omitempty"`
	Name      string `json:"name,omitempty"`
}

func parsePanelPageLimit(request *http.Request) (int, error) {
	rawLimit := strings.TrimSpace(request.URL.Query().Get("limit"))
	if rawLimit == "" {
		return defaultPanelPageLimit, nil
	}
	limit, err := strconv.Atoi(rawLimit)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("limit must be a positive integer")
	}
	if limit > maximumPanelPageLimit {
		limit = maximumPanelPageLimit
	}
	return limit, nil
}

func decodePanelCursor(rawCursor, expectedKind string) (*panelCursor, error) {
	rawCursor = strings.TrimSpace(rawCursor)
	if rawCursor == "" {
		return nil, nil
	}
	encodedPayload, err := base64.RawURLEncoding.DecodeString(rawCursor)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor encoding")
	}
	var cursor panelCursor
	if err := json.Unmarshal(encodedPayload, &cursor); err != nil {
		return nil, fmt.Errorf("invalid cursor payload")
	}
	if cursor.Kind != expectedKind {
		return nil, fmt.Errorf("cursor does not match this collection")
	}
	return &cursor, nil
}

func encodePanelCursor(cursor panelCursor) string {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func parseTimeIDCursor(request *http.Request, kind string) (*store.TimeIDCursor, error) {
	cursor, err := decodePanelCursor(request.URL.Query().Get("cursor"), kind)
	if err != nil || cursor == nil {
		return nil, err
	}
	if cursor.Timestamp == "" || cursor.StringID == "" {
		return nil, fmt.Errorf("cursor is missing its keyset boundary")
	}
	timestamp, err := time.Parse(time.RFC3339Nano, cursor.Timestamp)
	if err != nil {
		return nil, fmt.Errorf("cursor timestamp is invalid")
	}
	return &store.TimeIDCursor{Timestamp: timestamp.UTC(), ID: cursor.StringID}, nil
}

func encodeTimeIDCursor(kind string, cursor *store.TimeIDCursor) string {
	if cursor == nil {
		return ""
	}
	return encodePanelCursor(panelCursor{
		Kind:      kind,
		Timestamp: cursor.Timestamp.UTC().Format(time.RFC3339Nano),
		StringID:  cursor.ID,
	})
}

func parseTierCursor(request *http.Request) (*store.TierCursor, error) {
	cursor, err := decodePanelCursor(request.URL.Query().Get("cursor"), cursorKindTiers)
	if err != nil || cursor == nil {
		return nil, err
	}
	if cursor.Name == "" || cursor.StringID == "" || cursor.Level < 0 {
		return nil, fmt.Errorf("cursor is missing its keyset boundary")
	}
	return &store.TierCursor{Level: cursor.Level, Name: cursor.Name, ID: cursor.StringID}, nil
}

func encodeTierCursor(cursor *store.TierCursor) string {
	if cursor == nil {
		return ""
	}
	return encodePanelCursor(panelCursor{
		Kind:     cursorKindTiers,
		Level:    cursor.Level,
		Name:     cursor.Name,
		StringID: cursor.ID,
	})
}

func parseUsageRecordCursor(request *http.Request) (*store.UsageRecordCursor, error) {
	cursor, err := decodePanelCursor(request.URL.Query().Get("cursor"), cursorKindUsageRecords)
	if err != nil || cursor == nil {
		return nil, err
	}
	if cursor.Timestamp == "" || cursor.NumericID <= 0 {
		return nil, fmt.Errorf("cursor is missing its keyset boundary")
	}
	timestamp, err := time.Parse(time.RFC3339Nano, cursor.Timestamp)
	if err != nil {
		return nil, fmt.Errorf("cursor timestamp is invalid")
	}
	return &store.UsageRecordCursor{Timestamp: timestamp.UTC(), ID: cursor.NumericID}, nil
}

func encodeUsageRecordCursor(cursor *store.UsageRecordCursor) string {
	if cursor == nil {
		return ""
	}
	return encodePanelCursor(panelCursor{
		Kind:      cursorKindUsageRecords,
		Timestamp: cursor.Timestamp.UTC().Format(time.RFC3339Nano),
		NumericID: cursor.ID,
	})
}

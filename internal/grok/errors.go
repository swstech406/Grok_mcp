package grok

import (
	"errors"
	"fmt"
)

// UpstreamErrorCategory identifies a client-safe upstream failure class.
type UpstreamErrorCategory string

const (
	UpstreamErrorCategoryHTTPStatus   UpstreamErrorCategory = "http_status"
	UpstreamErrorCategoryResponseRead UpstreamErrorCategory = "response_read"
	UpstreamErrorCategoryStream       UpstreamErrorCategory = "stream"
	UpstreamErrorCategoryTransport    UpstreamErrorCategory = "transport"
)

// UpstreamError carries bounded operational metadata without retaining an
// upstream body, SSE payload, request URL, or credential in its error string.
type UpstreamError struct {
	Category      UpstreamErrorCategory
	Protocol      string
	Operation     string
	StatusCode    int
	BodyBytes     int
	BodyTruncated bool
	Event         string
	cause         error
}

func (upstreamError *UpstreamError) Error() string {
	if upstreamError == nil {
		return "upstream request failed"
	}
	switch upstreamError.Category {
	case UpstreamErrorCategoryHTTPStatus:
		return fmt.Sprintf("upstream returned HTTP %d", upstreamError.StatusCode)
	case UpstreamErrorCategoryResponseRead:
		return "upstream response read failed"
	case UpstreamErrorCategoryStream:
		return fmt.Sprintf(
			"upstream stream error event=%s payload_bytes=%d",
			upstreamError.Event,
			upstreamError.BodyBytes,
		)
	case UpstreamErrorCategoryTransport:
		return "upstream request failed"
	default:
		return "upstream request failed"
	}
}

func (upstreamError *UpstreamError) Unwrap() error {
	if upstreamError == nil {
		return nil
	}
	return upstreamError.cause
}

func newUpstreamTransportError(protocol, operation string, cause error) error {
	return &UpstreamError{
		Category:  UpstreamErrorCategoryTransport,
		Protocol:  protocol,
		Operation: operation,
		cause:     cause,
	}
}

func newUpstreamResponseReadError(protocol, operation string, statusCode, bodyBytes int, cause error) error {
	return &UpstreamError{
		Category:   UpstreamErrorCategoryResponseRead,
		Protocol:   protocol,
		Operation:  operation,
		StatusCode: statusCode,
		BodyBytes:  bodyBytes,
		cause:      cause,
	}
}

func newUpstreamStreamError(protocol, event string, payloadBytes int) error {
	return &UpstreamError{
		Category:  UpstreamErrorCategoryStream,
		Protocol:  protocol,
		Operation: "decode_stream",
		Event:     event,
		BodyBytes: payloadBytes,
	}
}

// UpstreamErrorMetadata returns typed safe metadata for logging and boundary
// classification. It never returns raw upstream content.
func UpstreamErrorMetadata(err error) (*UpstreamError, bool) {
	var upstreamError *UpstreamError
	if !errors.As(err, &upstreamError) {
		return nil, false
	}
	metadata := *upstreamError
	metadata.cause = nil
	return &metadata, true
}

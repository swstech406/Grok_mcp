package grok

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
)

const (
	maxUpstreamResponseBytes   int64 = 8 * 1024 * 1024
	maxSSEEventBytes                 = 1 * 1024 * 1024
	maxSSEEventCount                 = 4096
	maxSearchRoundCount              = 64
	maxAggregatedAnswerBytes         = 2 * 1024 * 1024
	maxCitationCount                 = 256
	maxAggregatedCitationBytes       = 256 * 1024
	maxSSELineBytes                  = maxSSEEventBytes + len("data: ") + 3
)

// forEachSSEEvent 按 SSE 规范解析响应体：空行分隔事件，多行 data: 拼接为一条 payload。
// 忽略 "[DONE]" 哨兵；对每条有效 JSON payload 调用 onEvent。
// payload 只在 onEvent 回调期间有效；需要跨事件保留时，调用方必须复制。
func forEachSSEEvent(r io.Reader, onEvent func(payload []byte) error) error {
	return forEachSSEEventWithDone(r, onEvent, nil)
}

// forEachSSEEventWithDone additionally reports each protocol-level [DONE]
// marker so callers that require a terminal event can reject truncated streams.
func forEachSSEEventWithDone(r io.Reader, onEvent func(payload []byte) error, onDone func() error) error {
	limitedReader := &io.LimitedReader{R: r, N: maxUpstreamResponseBytes + 1}
	scanner := bufio.NewScanner(limitedReader)
	// 为 data: 前缀、可选空格以及行尾预留空间，让 payload 上限由显式检查报告。
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSELineBytes)

	var eventBuffer bytes.Buffer
	dataLineCount := 0
	eventCount := 0

	flushEvent := func() error {
		if dataLineCount == 0 {
			return nil
		}
		payload := eventBuffer.Bytes()
		eventCount++
		if eventCount > maxSSEEventCount {
			return fmt.Errorf("upstream stream exceeded event limit of %d", maxSSEEventCount)
		}

		if bytes.Equal(payload, []byte("[DONE]")) {
			eventBuffer.Reset()
			dataLineCount = 0
			if onDone != nil {
				return onDone()
			}
			return nil
		}

		if err := onEvent(payload); err != nil {
			return err
		}
		eventBuffer.Reset()
		dataLineCount = 0
		return nil
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			if err := flushEvent(); err != nil {
				return err
			}
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			dataLine := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			additionalBytes := len(dataLine)
			if dataLineCount > 0 {
				additionalBytes++
			}
			if additionalBytes > maxSSEEventBytes || eventBuffer.Len() > maxSSEEventBytes-additionalBytes {
				return fmt.Errorf("upstream stream event exceeded byte limit of %d", maxSSEEventBytes)
			}
			if dataLineCount > 0 {
				eventBuffer.WriteByte('\n')
			}
			eventBuffer.Write(dataLine)
			dataLineCount++
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stream: %w", err)
	}
	if limitedReader.N == 0 {
		return fmt.Errorf("upstream stream exceeded total byte limit of %d", maxUpstreamResponseBytes)
	}
	return flushEvent()
}

func readAllUpstreamResponse(reader io.Reader) ([]byte, error) {
	limitedReader := &io.LimitedReader{R: reader, N: maxUpstreamResponseBytes + 1}
	responseBody, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, err
	}
	if int64(len(responseBody)) > maxUpstreamResponseBytes {
		return nil, fmt.Errorf("upstream response exceeded total byte limit of %d", maxUpstreamResponseBytes)
	}
	return responseBody, nil
}

func appendAnswerText(answer *strings.Builder, textParts ...string) error {
	additionalBytes := 0
	for _, textPart := range textParts {
		if additionalBytes > maxAggregatedAnswerBytes-len(textPart) {
			return fmt.Errorf("upstream response exceeded aggregated answer byte limit of %d", maxAggregatedAnswerBytes)
		}
		additionalBytes += len(textPart)
	}
	if answer.Len() > maxAggregatedAnswerBytes-additionalBytes {
		return fmt.Errorf("upstream response exceeded aggregated answer byte limit of %d", maxAggregatedAnswerBytes)
	}
	for _, textPart := range textParts {
		answer.WriteString(textPart)
	}
	return nil
}

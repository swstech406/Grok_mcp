package grok

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// forEachSSEEvent 按 SSE 规范解析响应体：空行分隔事件，多行 data: 拼接为一条 payload。
// 忽略 "[DONE]" 哨兵；对每条有效 JSON payload 调用 onEvent。
func forEachSSEEvent(r io.Reader, onEvent func(payload string) error) error {
	scanner := bufio.NewScanner(r)
	// 上游可能推送较大的 completed 事件，放宽 Scanner 缓冲区上限。
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var dataLines []string

	flushEvent := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = nil

		if payload == "[DONE]" {
			return nil
		}

		return onEvent(payload)
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flushEvent(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stream: %w", err)
	}
	return flushEvent()
}
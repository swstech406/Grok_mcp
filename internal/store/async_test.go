package store

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type blockingUsageStore struct {
	TestStore
	started chan struct{}
	unblock chan struct{}
}

func (s *blockingUsageStore) RecordUsage(context.Context, UsageRecord) error {
	select {
	case s.started <- struct{}{}:
	default:
	}
	<-s.unblock
	return nil
}

func TestAsyncUsageWriterStatsTrackDroppedRecords(t *testing.T) {
	blockingStore := &blockingUsageStore{
		started: make(chan struct{}, 1),
		unblock: make(chan struct{}),
	}
	writer := NewAsyncUsageWriter(blockingStore, 1)

	writer.Enqueue(UsageRecord{KeyID: "key-1", ToolName: "grok_web_search"})
	select {
	case <-blockingStore.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async writer to start blocked write")
	}

	writer.Enqueue(UsageRecord{KeyID: "key-2", ToolName: "grok_web_search"})
	droppedCapture, err := os.CreateTemp(t.TempDir(), "queue-full-debug-*.body")
	if err != nil {
		t.Fatal(err)
	}
	droppedCapturePath := droppedCapture.Name()
	if err := droppedCapture.Close(); err != nil {
		t.Fatal(err)
	}
	writer.Enqueue(UsageRecord{
		KeyID:    "key-3",
		ToolName: "grok_web_search",
		Cleanup: func() {
			_ = os.Remove(droppedCapturePath)
		},
	})
	writer.Enqueue(UsageRecord{KeyID: "key-4", ToolName: "grok_x_search"})
	if _, err := os.Stat(droppedCapturePath); !os.IsNotExist(err) {
		t.Fatalf("queue-full capture was not removed: %v", err)
	}

	stats := writer.Stats()
	if stats.DroppedRecords != 2 {
		t.Fatalf("dropped records = %d, want 2", stats.DroppedRecords)
	}
	if stats.QueueCapacity != 1 {
		t.Fatalf("queue capacity = %d, want 1", stats.QueueCapacity)
	}

	close(blockingStore.unblock)
	writer.Close()
}

func TestAsyncUsageWriterRateLimitsQueueFullLogs(t *testing.T) {
	blockingStore := &blockingUsageStore{
		started: make(chan struct{}, 1),
		unblock: make(chan struct{}),
	}
	writer := NewAsyncUsageWriter(blockingStore, 1)

	writer.Enqueue(UsageRecord{KeyID: "in-flight", ToolName: "grok_web_search"})
	select {
	case <-blockingStore.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async writer to start blocked write")
	}
	writer.Enqueue(UsageRecord{KeyID: "queued", ToolName: "grok_web_search"})

	var logOutput bytes.Buffer
	originalLogWriter := log.Writer()
	log.SetOutput(&logOutput)
	t.Cleanup(func() {
		log.SetOutput(originalLogWriter)
	})

	const droppedRequestCount = 20
	for requestIndex := 0; requestIndex < droppedRequestCount; requestIndex++ {
		writer.Enqueue(UsageRecord{KeyID: "dropped", ToolName: "grok_web_search"})
	}

	if stats := writer.Stats(); stats.DroppedRecords != droppedRequestCount {
		t.Fatalf("dropped records = %d, want %d", stats.DroppedRecords, droppedRequestCount)
	}
	if logCount := strings.Count(logOutput.String(), "usage record dropped (buffer full)"); logCount != 1 {
		t.Fatalf("queue-full log count = %d, want 1; logs=%q", logCount, logOutput.String())
	}

	close(blockingStore.unblock)
	writer.Close()
}

type failingUsageStore struct {
	TestStore
	recordUsageErr error
}

func (s failingUsageStore) RecordUsage(context.Context, UsageRecord) error {
	return s.recordUsageErr
}

func TestAsyncUsageWriterStatsTrackWriteFailures(t *testing.T) {
	temporaryCapture, err := os.CreateTemp(t.TempDir(), "write-failure-debug-*.body")
	if err != nil {
		t.Fatal(err)
	}
	temporaryCapturePath := temporaryCapture.Name()
	if err := temporaryCapture.Close(); err != nil {
		t.Fatal(err)
	}

	writer := NewAsyncUsageWriter(failingUsageStore{recordUsageErr: errors.New("db unavailable")}, 1)
	writer.Enqueue(UsageRecord{
		KeyID:    "key-1",
		ToolName: "grok_web_search",
		Cleanup: func() {
			_ = os.Remove(temporaryCapturePath)
		},
	})
	writer.Close()

	stats := writer.Stats()
	if stats.WriteFailures != 1 {
		t.Fatalf("write failures = %d, want 1", stats.WriteFailures)
	}
	if stats.WriteSuccesses != 0 {
		t.Fatalf("write successes = %d, want 0", stats.WriteSuccesses)
	}
	if _, err := os.Stat(temporaryCapturePath); !os.IsNotExist(err) {
		t.Fatalf("write-failure capture was not removed: %v", err)
	}
}

func TestAsyncUsageWriterCleansCaptureRejectedAfterClose(t *testing.T) {
	writer := NewAsyncUsageWriter(TestStore{}, 1)
	writer.Close()

	temporaryCapture, err := os.CreateTemp(t.TempDir(), "post-close-debug-*.body")
	if err != nil {
		t.Fatal(err)
	}
	temporaryCapturePath := temporaryCapture.Name()
	if err := temporaryCapture.Close(); err != nil {
		t.Fatal(err)
	}
	writer.Enqueue(UsageRecord{
		KeyID:    "post-close",
		ToolName: "grok_web_search",
		Cleanup: func() {
			_ = os.Remove(temporaryCapturePath)
		},
	})
	if _, err := os.Stat(temporaryCapturePath); !os.IsNotExist(err) {
		t.Fatalf("post-close capture was not removed: %v", err)
	}
}

type deadlineAwareUsageStore struct {
	TestStore
	started chan struct{}
}

func (s *deadlineAwareUsageStore) RecordUsage(ctx context.Context, _ UsageRecord) error {
	select {
	case s.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestAsyncUsageWriterAppliesPerWriteDeadline(t *testing.T) {
	deadlineStore := &deadlineAwareUsageStore{started: make(chan struct{}, 1)}
	writer := newAsyncUsageWriter(deadlineStore, 1, 30*time.Millisecond, 250*time.Millisecond)
	writer.Enqueue(UsageRecord{KeyID: "key-1", ToolName: "grok_web_search"})

	startedAt := time.Now()
	writer.Close()
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("Close took %s, expected the write deadline to bound it", elapsed)
	}
	if stats := writer.Stats(); stats.WriteFailures != 1 {
		t.Fatalf("write failures = %d, want 1", stats.WriteFailures)
	}
}

func TestAsyncUsageWriterCloseIsBoundedAndCleansQueuedCapture(t *testing.T) {
	blockingStore := &blockingUsageStore{
		started: make(chan struct{}, 1),
		unblock: make(chan struct{}),
	}
	writer := newAsyncUsageWriter(blockingStore, 2, 20*time.Millisecond, 50*time.Millisecond)
	writer.Enqueue(UsageRecord{KeyID: "in-flight", ToolName: "grok_web_search"})
	select {
	case <-blockingStore.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the in-flight write")
	}

	temporaryCapture, err := os.CreateTemp(t.TempDir(), "queued-debug-*.json")
	if err != nil {
		t.Fatal(err)
	}
	temporaryCapturePath := temporaryCapture.Name()
	if err := temporaryCapture.Close(); err != nil {
		t.Fatal(err)
	}
	writer.Enqueue(UsageRecord{
		KeyID:    "queued",
		ToolName: "grok_x_search",
		Cleanup: func() {
			_ = os.Remove(temporaryCapturePath)
		},
	})

	startedAt := time.Now()
	writer.Close()
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("Close took %s, want a bounded shutdown", elapsed)
	}
	if _, err := os.Stat(temporaryCapturePath); !os.IsNotExist(err) {
		t.Fatalf("queued temporary capture was not removed, stat error: %v", err)
	}
	if stats := writer.Stats(); stats.DroppedRecords != 1 {
		t.Fatalf("dropped records = %d, want 1 queued record discarded at shutdown", stats.DroppedRecords)
	}

	close(blockingStore.unblock)
	select {
	case <-writer.workerDone:
	case <-time.After(time.Second):
		t.Fatal("worker did not exit after the blocking store returned")
	}
}

func TestAsyncUsageWriterCloseRacesSafelyWithAdmission(t *testing.T) {
	writer := newAsyncUsageWriter(TestStore{}, 8, 100*time.Millisecond, 250*time.Millisecond)
	const enqueueCount = 128
	start := make(chan struct{})
	var operations sync.WaitGroup
	var cleanupCount atomic.Int64

	operations.Add(enqueueCount + 1)
	for recordIndex := 0; recordIndex < enqueueCount; recordIndex++ {
		go func() {
			defer operations.Done()
			<-start
			writer.Enqueue(UsageRecord{
				KeyID:    "racing-key",
				ToolName: "grok_web_search",
				Cleanup: func() {
					cleanupCount.Add(1)
				},
			})
		}()
	}
	go func() {
		defer operations.Done()
		<-start
		writer.Close()
	}()

	close(start)
	operations.Wait()
	writer.Close()
	if got := cleanupCount.Load(); got != enqueueCount {
		t.Fatalf("cleanup count = %d, want %d", got, enqueueCount)
	}
}

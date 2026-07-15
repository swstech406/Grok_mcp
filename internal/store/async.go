package store

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultAsyncUsageWriteTimeout = 2 * time.Second
	defaultAsyncUsageCloseTimeout = 3 * time.Second
	asyncUsageCancellationGrace   = 250 * time.Millisecond
	asyncUsageDropLogInterval     = time.Second
)

// AsyncUsageWriter 将用量写入从请求路径解耦：主线程只入队，后台 goroutine 调用 Store。
type AsyncUsageWriter struct {
	store        Store
	ch           chan UsageRecord
	writeTimeout time.Duration
	closeTimeout time.Duration
	cancelWorker context.CancelFunc
	workerDone   chan struct{}

	admissionMu sync.Mutex
	accepting   bool
	closeOnce   sync.Once

	// 可观测计数：缓冲丢弃与写库失败/成功（原子累加，便于运维与测试断言）。
	droppedRecords atomic.Uint64
	writeFailures  atomic.Uint64
	writeSuccesses atomic.Uint64
	nextDropLogAt  atomic.Int64
}

// AsyncUsageWriterStats 是异步用量写入器的快照统计。
type AsyncUsageWriterStats struct {
	DroppedRecords uint64
	WriteFailures  uint64
	WriteSuccesses uint64
	QueueLength    int
	QueueCapacity  int
}

// NewAsyncUsageWriter 启动消费者；buffer 为满时 Enqueue 会丢弃并限频记录日志（不阻塞 MCP）。
func NewAsyncUsageWriter(s Store, buffer int) *AsyncUsageWriter {
	return newAsyncUsageWriter(s, buffer, defaultAsyncUsageWriteTimeout, defaultAsyncUsageCloseTimeout)
}

func newAsyncUsageWriter(s Store, buffer int, writeTimeout, closeTimeout time.Duration) *AsyncUsageWriter {
	if buffer <= 0 {
		buffer = 256
	}
	if writeTimeout <= 0 {
		writeTimeout = defaultAsyncUsageWriteTimeout
	}
	if closeTimeout <= 0 {
		closeTimeout = defaultAsyncUsageCloseTimeout
	}
	workerContext, cancelWorker := context.WithCancel(context.Background())
	writer := &AsyncUsageWriter{
		store:        s,
		ch:           make(chan UsageRecord, buffer),
		writeTimeout: writeTimeout,
		closeTimeout: closeTimeout,
		cancelWorker: cancelWorker,
		workerDone:   make(chan struct{}),
		accepting:    true,
	}
	go writer.run(workerContext)
	return writer
}

func (w *AsyncUsageWriter) run(ctx context.Context) {
	defer close(w.workerDone)
	for {
		select {
		case <-ctx.Done():
			w.discardQueuedRecords("shutdown deadline reached")
			return
		case rec, ok := <-w.ch:
			if !ok {
				return
			}
			if ctx.Err() != nil {
				w.discardRecord(rec, "shutdown deadline reached")
				w.discardQueuedRecords("shutdown deadline reached")
				return
			}
			w.write(ctx, rec)
		}
	}
}

func (w *AsyncUsageWriter) write(workerContext context.Context, rec UsageRecord) {
	defer cleanupUsageRecord(rec)

	writeContext, cancelWrite := context.WithTimeout(workerContext, w.writeTimeout)
	defer cancelWrite()
	if err := w.store.RecordUsage(writeContext, rec); err != nil {
		failures := w.writeFailures.Add(1)
		log.Printf("usage record write failed key=%s tool=%s failures=%d: %v", rec.KeyID, rec.ToolName, failures, err)
		return
	}
	w.writeSuccesses.Add(1)
}

// Enqueue 非阻塞入队；channel 已满时丢弃本条记录并累加计数。
func (w *AsyncUsageWriter) Enqueue(rec UsageRecord) {
	if w == nil {
		cleanupUsageRecord(rec)
		return
	}

	w.admissionMu.Lock()
	if !w.accepting {
		w.admissionMu.Unlock()
		w.discardRecord(rec, "writer closed")
		return
	}

	admitted := false
	select {
	case w.ch <- rec:
		admitted = true
	default:
	}
	w.admissionMu.Unlock()
	if !admitted {
		w.discardRecord(rec, "buffer full")
	}
}

func (w *AsyncUsageWriter) discardQueuedRecords(reason string) {
	for {
		select {
		case rec, ok := <-w.ch:
			if !ok {
				return
			}
			w.discardRecord(rec, reason)
		default:
			return
		}
	}
}

func (w *AsyncUsageWriter) discardRecord(rec UsageRecord, reason string) {
	defer cleanupUsageRecord(rec)
	dropped := w.droppedRecords.Add(1)
	if !w.shouldLogDrop() {
		return
	}
	log.Printf("usage record dropped (%s) key=%s tool=%s dropped_records=%d queue_cap=%d",
		reason, rec.KeyID, rec.ToolName, dropped, cap(w.ch))
}

func (w *AsyncUsageWriter) shouldLogDrop() bool {
	now := time.Now()
	nowUnixNano := now.UnixNano()
	for {
		nextAllowedUnixNano := w.nextDropLogAt.Load()
		if nowUnixNano < nextAllowedUnixNano {
			return false
		}
		if w.nextDropLogAt.CompareAndSwap(nextAllowedUnixNano, now.Add(asyncUsageDropLogInterval).UnixNano()) {
			return true
		}
	}
}

func cleanupUsageRecord(rec UsageRecord) {
	if rec.Cleanup == nil {
		return
	}
	defer func() {
		if recoveredValue := recover(); recoveredValue != nil {
			log.Printf("usage record cleanup panicked key=%s tool=%s: %v", rec.KeyID, rec.ToolName, recoveredValue)
		}
	}()
	rec.Cleanup()
}

// Stats 返回丢弃/写库计数与当前队列深度快照。
func (w *AsyncUsageWriter) Stats() AsyncUsageWriterStats {
	return AsyncUsageWriterStats{
		DroppedRecords: w.droppedRecords.Load(),
		WriteFailures:  w.writeFailures.Load(),
		WriteSuccesses: w.writeSuccesses.Load(),
		QueueLength:    len(w.ch),
		QueueCapacity:  cap(w.ch),
	}
}

// Close stops admission, gives the worker a bounded interval to flush queued
// records, then cancels any active write and cleans up records still queued.
// It must be called before Store.Close.
func (w *AsyncUsageWriter) Close() {
	if w == nil {
		return
	}
	w.closeOnce.Do(func() {
		w.admissionMu.Lock()
		w.accepting = false
		close(w.ch)
		w.admissionMu.Unlock()

		closeTimer := time.NewTimer(w.closeTimeout)
		defer closeTimer.Stop()
		select {
		case <-w.workerDone:
			w.cancelWorker()
			return
		case <-closeTimer.C:
		}

		w.cancelWorker()
		w.discardQueuedRecords("shutdown deadline reached")

		cancellationGrace := asyncUsageCancellationGrace
		if w.writeTimeout < cancellationGrace {
			cancellationGrace = w.writeTimeout
		}
		graceTimer := time.NewTimer(cancellationGrace)
		defer graceTimer.Stop()
		select {
		case <-w.workerDone:
		case <-graceTimer.C:
			log.Printf("async usage writer close timed out with an in-flight store write")
		}
	})
}

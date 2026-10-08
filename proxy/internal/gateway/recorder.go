package gateway

import (
	"fmt"
	"log/slog"
	"sync"
)

// BatchSink is an optional TelemetrySink capability: write several rows at
// once (one transaction for the local store). It must not keep the slice.
type BatchSink interface {
	RecordBatch([]RequestRecord)
}

const (
	// recordQueueMax bounds rows recorded but not yet written, and so the
	// request bodies their token counts still hold. A full queue makes the
	// next record wait: back-pressure, never a dropped row.
	recordQueueMax = 64
	// recordBatchMax bounds the rows one sink write carries, and so how long
	// one transaction holds the database's write lock.
	recordBatchMax = 64
)

type finishedRecord struct {
	row     RequestRecord
	observe bool
	// dropped: finishing the row panicked, so it is not written, as a row
	// whose handler panicked never was.
	dropped bool
	// flushed marks a Flush barrier, closed once every row before it is written.
	flushed chan struct{}
}

// recorder finishes rows off the request path (Config.AsyncRecord). Each
// row's request token count runs on its own goroutine, so one large request
// does not hold up the rows behind it, and a single writer hands finished
// rows to the sink in the order they were recorded.
type recorder struct {
	write  func([]finishedRecord)
	logger *slog.Logger
	mu     sync.RWMutex
	closed bool
	queue  chan chan finishedRecord
	done   chan struct{}
}

func newRecorder(write func([]finishedRecord), logger *slog.Logger) *recorder {
	r := &recorder{write: write, logger: logger, queue: make(chan chan finishedRecord, recordQueueMax), done: make(chan struct{})}
	go r.run()
	return r
}

// submit queues row, then finishes it with account on its own goroutine. It
// reports false once the recorder is closed; the caller finishes inline.
func (r *recorder) submit(row RequestRecord, observe bool, account func(*RequestRecord)) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return false
	}
	result := make(chan finishedRecord, 1)
	r.queue <- result
	go func() {
		finished := finishedRecord{row: row, observe: observe}
		defer func() {
			if v := recover(); v != nil {
				r.warn("telemetry row dropped: finishing it panicked", "panic", fmt.Sprintf("%T", v), "request_id", row.RequestID)
				finished = finishedRecord{dropped: true}
			}
			result <- finished
		}()
		account(&finished.row)
	}()
	return true
}

// flush returns once every row submitted before it is written.
func (r *recorder) flush() {
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		<-r.done
		return
	}
	barrier, result := make(chan struct{}), make(chan finishedRecord, 1)
	result <- finishedRecord{flushed: barrier}
	r.queue <- result
	r.mu.RUnlock()
	<-barrier
}

func (r *recorder) run() {
	defer close(r.done)
	var pending []finishedRecord
	for result := range r.queue {
		select {
		case finished := <-result:
			pending = append(pending, finished)
		default: // still being counted: write the rows already finished first
			r.writeBatch(pending)
			pending = append(pending[:0], <-result)
		}
		if len(pending) >= recordBatchMax || len(r.queue) == 0 {
			r.writeBatch(pending)
			pending = pending[:0]
		}
	}
}

func (r *recorder) writeBatch(batch []finishedRecord) {
	var rows []finishedRecord
	for _, finished := range batch {
		if !finished.dropped && finished.flushed == nil {
			rows = append(rows, finished)
		}
	}
	if len(rows) > 0 {
		func() {
			defer func() {
				if v := recover(); v != nil {
					r.warn("telemetry rows dropped: writing them panicked", "panic", fmt.Sprintf("%T", v), "rows", len(rows))
				}
			}()
			r.write(rows)
		}()
	}
	for _, finished := range batch {
		if finished.flushed != nil {
			close(finished.flushed)
		}
	}
}

func (r *recorder) warn(msg string, args ...any) {
	if r.logger != nil {
		r.logger.Warn(msg, args...)
	}
}

// close writes every row submitted so far and returns once they are written.
func (r *recorder) close() {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.queue)
	}
	r.mu.Unlock()
	<-r.done
}

// finishRecord completes row (account adds the request token count) and
// hands it to the sink and, with observe, to the Cloud link. With a recorder
// that happens off the request path; without one, before it returns.
func (s *Server) finishRecord(row RequestRecord, observe bool, account func(*RequestRecord)) {
	if s.recorder != nil && s.recorder.submit(row, observe, account) {
		return
	}
	account(&row)
	s.sink.Record(row)
	if observe && s.cloud != nil {
		s.cloud.Observe(row)
	}
}

// writeRecords is the recorder's sink write: one call for the batch when the
// sink takes batches.
func (s *Server) writeRecords(batch []finishedRecord) {
	if sink, ok := s.sink.(BatchSink); ok {
		rows := make([]RequestRecord, len(batch))
		for i := range batch {
			rows[i] = batch[i].row
		}
		sink.RecordBatch(rows)
	} else {
		for i := range batch {
			s.sink.Record(batch[i].row)
		}
	}
	if s.cloud != nil {
		for i := range batch {
			if batch[i].observe {
				s.cloud.Observe(batch[i].row)
			}
		}
	}
}

// Flush returns once every row recorded so far is in the sink. A reader that
// must see the request it follows (a session's receipt) calls it first.
// Without Config.AsyncRecord the rows are already there.
func (s *Server) Flush() {
	if s.recorder != nil {
		s.recorder.flush()
	}
}

// Close writes out the rows Config.AsyncRecord is still finishing. Call it
// after the HTTP server has shut down and before the sink closes; a request
// still running after Close finishes its row inline.
func (s *Server) Close() error {
	if s.warmer != nil {
		s.warmer.close() // first: a warm in flight still records a row
	}
	if s.recorder != nil {
		s.recorder.close()
	}
	return nil
}

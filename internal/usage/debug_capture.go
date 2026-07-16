package usage

import (
	"io"
	"net/http"
	"os"
	"strings"
)

type debugBodySpool struct {
	file          *os.File
	path          string
	bytesCount    int64
	observedBytes int64
	truncated     bool
	writeErr      error
	closed        bool
}

// Debug capture is intentionally a bounded diagnostic sample. Request and
// response forwarding remain unbounded; only the bytes retained for later
// inspection are capped.
const maxDebugCapturedBodyBytes int64 = 1 << 20 // 1 MiB per direction

func createDebugBodySpool(bodyKind string) (*debugBodySpool, error) {
	bodyFile, err := os.CreateTemp("", "grok-search-mcp-debug-"+bodyKind+"-*.body")
	if err != nil {
		return nil, err
	}
	if err := bodyFile.Chmod(0o600); err != nil {
		bodyPath := bodyFile.Name()
		_ = bodyFile.Close()
		_ = os.Remove(bodyPath)
		return nil, err
	}
	return &debugBodySpool{file: bodyFile, path: bodyFile.Name()}, nil
}

func (s *debugBodySpool) write(body []byte) {
	if s == nil || len(body) == 0 {
		return
	}
	s.observedBytes += int64(len(body))
	if s.file == nil || s.writeErr != nil {
		return
	}

	remainingCaptureBytes := maxDebugCapturedBodyBytes - s.bytesCount
	if remainingCaptureBytes <= 0 {
		s.truncated = true
		return
	}
	if int64(len(body)) > remainingCaptureBytes {
		body = body[:int(remainingCaptureBytes)]
		s.truncated = true
	}
	for len(body) > 0 {
		bytesWritten, err := s.file.Write(body)
		if bytesWritten > 0 {
			s.bytesCount += int64(bytesWritten)
			body = body[bytesWritten:]
		}
		if err != nil {
			s.writeErr = err
			return
		}
		if bytesWritten == 0 {
			s.writeErr = io.ErrShortWrite
			return
		}
	}
}

func (s *debugBodySpool) close() {
	if s == nil || s.file == nil || s.closed {
		return
	}
	s.closed = true
	if err := s.file.Close(); err != nil && s.writeErr == nil {
		s.writeErr = err
	}
}

func (s *debugBodySpool) cleanup() {
	if s == nil {
		return
	}
	s.close()
	if s.path != "" {
		_ = os.Remove(s.path)
	}
}

type debugCaptureReadCloser struct {
	source io.ReadCloser
	spool  *debugBodySpool
}

func (r *debugCaptureReadCloser) Read(destination []byte) (int, error) {
	bytesRead, readErr := r.source.Read(destination)
	if bytesRead > 0 {
		r.spool.write(destination[:bytesRead])
	}
	return bytesRead, readErr
}

// The middleware owns the source body until capture finalization so downstream
// code cannot close it before the capture session records its final state.
func (r *debugCaptureReadCloser) Close() error {
	return nil
}

type debugCaptureSession struct {
	requestSpool       *debugBodySpool
	responseSpool      *debugBodySpool
	requestReader      *debugCaptureReadCloser
	requestCaptureErr  error
	responseCaptureErr error
	finalized          bool
}

func startDebugCapture(request *http.Request) *debugCaptureSession {
	session := &debugCaptureSession{}
	session.requestSpool, session.requestCaptureErr = createDebugBodySpool("request")
	session.responseSpool, session.responseCaptureErr = createDebugBodySpool("response")
	if request.Body != nil && session.requestSpool != nil {
		session.requestReader = &debugCaptureReadCloser{
			source: request.Body,
			spool:  session.requestSpool,
		}
		request.Body = session.requestReader
	}
	return session
}

func (s *debugCaptureSession) finalize() {
	if s == nil || s.finalized {
		return
	}
	s.finalized = true
	if s.requestReader != nil {
		if err := s.requestReader.source.Close(); err != nil && s.requestSpool.writeErr == nil {
			s.requestSpool.writeErr = err
		}
		s.requestReader.source = nil
		s.requestReader = nil
	}
	s.requestSpool.close()
	s.responseSpool.close()
}

func (s *debugCaptureSession) cleanup() {
	if s == nil {
		return
	}
	s.requestSpool.cleanup()
	s.responseSpool.cleanup()
}

func (s *debugCaptureSession) requestPath() string {
	if s == nil || s.requestSpool == nil || s.requestSpool.writeErr != nil {
		return ""
	}
	return s.requestSpool.path
}

func (s *debugCaptureSession) responsePath() string {
	if s == nil || s.responseSpool == nil || s.responseSpool.writeErr != nil {
		return ""
	}
	return s.responseSpool.path
}

func (s *debugCaptureSession) requestBytes() int64 {
	if s == nil || s.requestSpool == nil {
		return 0
	}
	return s.requestSpool.bytesCount
}

func (s *debugCaptureSession) responseBytes() int64 {
	if s == nil || s.responseSpool == nil {
		return 0
	}
	return s.responseSpool.bytesCount
}

func (s *debugCaptureSession) requestObservedBytes() int64 {
	if s == nil || s.requestSpool == nil {
		return 0
	}
	return s.requestSpool.observedBytes
}

func (s *debugCaptureSession) responseObservedBytes() int64 {
	if s == nil || s.responseSpool == nil {
		return 0
	}
	return s.responseSpool.observedBytes
}

func (s *debugCaptureSession) requestTruncated() bool {
	return s != nil && s.requestSpool != nil && s.requestSpool.truncated
}

func (s *debugCaptureSession) responseTruncated() bool {
	return s != nil && s.responseSpool != nil && s.responseSpool.truncated
}

func (s *debugCaptureSession) captureError() string {
	if s == nil {
		return ""
	}
	errors := make([]string, 0, 2)
	requestErr := s.requestCaptureErr
	if requestErr == nil && s.requestSpool != nil {
		requestErr = s.requestSpool.writeErr
	}
	if requestErr != nil {
		errors = append(errors, "request: "+requestErr.Error())
	}
	responseErr := s.responseCaptureErr
	if responseErr == nil && s.responseSpool != nil {
		responseErr = s.responseSpool.writeErr
	}
	if responseErr != nil {
		errors = append(errors, "response: "+responseErr.Error())
	}
	return strings.Join(errors, "; ")
}

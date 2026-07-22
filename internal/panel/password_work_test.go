package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/config"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

type passwordWorkRegistrationStore struct {
	store.TestStore
}

func (passwordWorkRegistrationStore) GetServerSettings(context.Context) (*store.ServerSettings, error) {
	return &store.ServerSettings{Runtime: config.ServerSettings{RegistrationMode: store.RegistrationModeFree}}, nil
}

func (passwordWorkRegistrationStore) GetUserByUsername(context.Context, string) (*store.User, error) {
	return nil, nil
}

func (passwordWorkRegistrationStore) RegisterUserWithCurrentMode(
	_ context.Context,
	username string,
	_ string,
	_ string,
	_ store.RegistrationMode,
) (*store.User, error) {
	return &store.User{ID: username + "-id", Username: username, Enabled: true, Role: store.RoleUser}, nil
}

func TestPasswordWorkAdmissionBoundsConcurrentLoginComparison(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{PasswordMaximumConcurrent: 1})
	comparisonStarted := make(chan struct{})
	releaseComparison := make(chan struct{})
	var startOnce sync.Once
	handler := &Handler{
		Store:         &loginLookupCountingStore{},
		AuthProtector: authProtector,
		passwordHashComparator: func(context.Context, []byte, []byte) error {
			startOnce.Do(func() { close(comparisonStarted) })
			<-releaseComparison
			return errors.New("invalid password")
		},
	}

	firstResponse := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.login(response, newPasswordWorkLoginRequest("first-user", "198.51.100.10:1234"))
		firstResponse <- response
	}()
	select {
	case <-comparisonStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first password comparison")
	}

	secondResponse := httptest.NewRecorder()
	handler.login(secondResponse, newPasswordWorkLoginRequest("second-user", "198.51.100.11:1234"))
	if secondResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("second login status = %d, want %d", secondResponse.Code, http.StatusServiceUnavailable)
	}
	if secondResponse.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q, want 1", secondResponse.Header().Get("Retry-After"))
	}
	close(releaseComparison)
	if response := <-firstResponse; response.Code != http.StatusUnauthorized {
		t.Fatalf("first login status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	metrics := authProtector.Metrics().PasswordWork
	if metrics.CurrentWork != 0 || metrics.Capacity != 1 || metrics.Admissions != 1 || metrics.Rejections != 1 {
		t.Fatalf("password work metrics = %+v", metrics)
	}
}

func TestPasswordWorkAdmissionBoundsRegistrationHashGeneration(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{
		PasswordMaximumConcurrent:          1,
		RegistrationProofDifficultyBits:    4,
		RegistrationProofValidity:          time.Minute,
		RegistrationProofMaxUsedChallenges: 4,
	})
	hashStarted := make(chan struct{})
	releaseHash := make(chan struct{})
	var startOnce sync.Once
	handler := &Handler{
		Store:         passwordWorkRegistrationStore{},
		AuthProtector: authProtector,
		passwordHashGenerator: func([]byte, int) ([]byte, error) {
			startOnce.Do(func() { close(hashStarted) })
			<-releaseHash
			return []byte("password-hash"), nil
		},
	}

	firstRequest := newPasswordWorkRegistrationRequest(t, authProtector, "first-user")
	firstResponse := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.register(response, firstRequest)
		firstResponse <- response
	}()
	select {
	case <-hashStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first password hash")
	}

	secondResponse := httptest.NewRecorder()
	handler.register(secondResponse, newPasswordWorkRegistrationRequest(t, authProtector, "second-user"))
	if secondResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("second registration status = %d, want %d", secondResponse.Code, http.StatusServiceUnavailable)
	}
	if secondResponse.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q, want 1", secondResponse.Header().Get("Retry-After"))
	}
	close(releaseHash)
	if response := <-firstResponse; response.Code != http.StatusCreated {
		t.Fatalf("first registration status = %d, want %d", response.Code, http.StatusCreated)
	}
}

func TestPasswordWorkAdmissionReleasesAfterPanicAndIsIdempotent(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{PasswordMaximumConcurrent: 1})
	release := authProtector.tryAcquirePasswordWork()
	if release == nil {
		t.Fatal("initial password work admission was rejected")
	}
	release()
	release()
	if currentWork := authProtector.Metrics().PasswordWork.CurrentWork; currentWork != 0 {
		t.Fatalf("current password work after repeated release = %d, want 0", currentWork)
	}

	handler := &Handler{
		Store:         &loginLookupCountingStore{},
		AuthProtector: authProtector,
		passwordHashComparator: func(context.Context, []byte, []byte) error {
			panic("comparison panic")
		},
	}
	func() {
		defer func() {
			if recoveredValue := recover(); recoveredValue == nil {
				t.Fatal("password comparison panic was not propagated")
			}
		}()
		handler.login(httptest.NewRecorder(), newPasswordWorkLoginRequest("panic-user", "198.51.100.20:1234"))
	}()
	if currentWork := authProtector.Metrics().PasswordWork.CurrentWork; currentWork != 0 {
		t.Fatalf("current password work after panic = %d, want 0", currentWork)
	}

	handler.passwordHashComparator = func(context.Context, []byte, []byte) error { return errors.New("invalid password") }
	response := httptest.NewRecorder()
	handler.login(response, newPasswordWorkLoginRequest("after-panic", "198.51.100.21:1234"))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("login after panic status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestPasswordWorkAdmissionReleasesWhenInjectedComparisonHonorsCancellation(t *testing.T) {
	authProtector := NewAuthProtector(AuthProtectorConfig{PasswordMaximumConcurrent: 1})
	comparisonStarted := make(chan struct{})
	handler := &Handler{
		Store:         &loginLookupCountingStore{},
		AuthProtector: authProtector,
		passwordHashComparator: func(requestContext context.Context, _ []byte, _ []byte) error {
			close(comparisonStarted)
			<-requestContext.Done()
			return requestContext.Err()
		},
	}
	requestContext, cancelRequest := context.WithCancel(context.Background())
	request := newPasswordWorkLoginRequest("cancelled-user", "198.51.100.30:1234").WithContext(requestContext)
	responseDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.login(response, request)
		responseDone <- response
	}()
	select {
	case <-comparisonStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cancellable password comparison")
	}
	cancelRequest()
	if response := <-responseDone; response.Code != http.StatusUnauthorized {
		t.Fatalf("cancelled login status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if currentWork := authProtector.Metrics().PasswordWork.CurrentWork; currentWork != 0 {
		t.Fatalf("current password work after cancellation = %d, want 0", currentWork)
	}
	release := authProtector.tryAcquirePasswordWork()
	if release == nil {
		t.Fatal("password work remained saturated after cancellation")
	}
	release()
}

func newPasswordWorkLoginRequest(username, remoteAddress string) *http.Request {
	request := httptest.NewRequest(
		http.MethodPost,
		"/panel/v1/auth/login",
		bytes.NewBufferString(`{"username":"`+username+`","password":"password123"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = remoteAddress
	return request
}

func newPasswordWorkRegistrationRequest(
	t *testing.T,
	authProtector *AuthProtector,
	username string,
) *http.Request {
	t.Helper()
	challenge, err := authProtector.registrationProof.issue(authProtector.now())
	if err != nil {
		t.Fatal(err)
	}
	requestBody, err := json.Marshal(RegisterRequest{
		Username: username,
		Password: "password123",
		Proof:    solveRegistrationProofForStateTest(challenge, username, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/panel/v1/auth/register", bytes.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	return request
}

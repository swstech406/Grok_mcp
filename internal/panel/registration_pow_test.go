package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/config"
	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
)

func TestRegistrationProofVerifiesOnceAndBindsRegistrationData(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	proofState := newRegistrationProofState(4, time.Minute, 0, now)
	challenge, err := proofState.issue(now)
	if err != nil {
		t.Fatal(err)
	}

	proof := solveBoundRegistrationProofForStateTest(challenge)
	if err := proofState.verifyAndConsume(now, proof, "alice", "different-invite-code"); !errors.Is(err, errRegistrationProofInvalid) {
		t.Fatalf("proof with changed invite code error = %v, want %v", err, errRegistrationProofInvalid)
	}
	if err := proofState.verifyAndConsume(now, proof, "different-user", "invite-code"); !errors.Is(err, errRegistrationProofInvalid) {
		t.Fatalf("proof with changed username error = %v, want %v", err, errRegistrationProofInvalid)
	}
	if err := proofState.verifyAndConsume(now, proof, "alice", "invite-code"); err != nil {
		t.Fatalf("valid proof error = %v", err)
	}
	if err := proofState.verifyAndConsume(now, proof, "alice", "invite-code"); !errors.Is(err, errRegistrationProofReplayed) {
		t.Fatalf("replayed proof error = %v, want %v", err, errRegistrationProofReplayed)
	}
}

func solveBoundRegistrationProofForStateTest(challenge RegistrationChallengeResponse) RegistrationProof {
	for nonce := uint64(0); ; nonce++ {
		validDigest := calculateRegistrationProofDigest(challenge.Challenge, "alice", "invite-code", nonce)
		changedInviteDigest := calculateRegistrationProofDigest(challenge.Challenge, "alice", "different-invite-code", nonce)
		changedUsernameDigest := calculateRegistrationProofDigest(challenge.Challenge, "different-user", "invite-code", nonce)
		if hasLeadingZeroBits(validDigest, challenge.Difficulty) &&
			!hasLeadingZeroBits(changedInviteDigest, challenge.Difficulty) &&
			!hasLeadingZeroBits(changedUsernameDigest, challenge.Difficulty) {
			return RegistrationProof{
				Challenge: challenge.Challenge,
				Nonce:     strconv.FormatUint(nonce, 10),
			}
		}
	}
}

func TestRegistrationProofRejectsMissingTamperedAndExpiredChallenges(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	proofState := newRegistrationProofState(4, time.Minute, 0, now)

	if err := proofState.verifyAndConsume(now, RegistrationProof{}, "alice", ""); !errors.Is(err, errRegistrationProofRequired) {
		t.Fatalf("missing proof error = %v, want %v", err, errRegistrationProofRequired)
	}

	challenge, err := proofState.issue(now)
	if err != nil {
		t.Fatal(err)
	}
	proof := solveRegistrationProofForStateTest(challenge, "alice", "")
	proof.Challenge += "tampered"
	if err := proofState.verifyAndConsume(now, proof, "alice", ""); !errors.Is(err, errRegistrationProofInvalid) {
		t.Fatalf("tampered challenge error = %v, want %v", err, errRegistrationProofInvalid)
	}

	expiringState := newRegistrationProofState(4, time.Second, 0, now)
	expiringChallenge, err := expiringState.issue(now)
	if err != nil {
		t.Fatal(err)
	}
	expiredProof := solveRegistrationProofForStateTest(expiringChallenge, "alice", "")
	if err := expiringState.verifyAndConsume(now.Add(2*time.Second), expiredProof, "alice", ""); !errors.Is(err, errRegistrationProofExpired) {
		t.Fatalf("expired proof error = %v, want %v", err, errRegistrationProofExpired)
	}
}

func TestRegistrationProofRejectsNewConsumptionAtCapacity(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	proofState := newRegistrationProofState(4, time.Minute, 1, now)

	firstChallenge, err := proofState.issue(now)
	if err != nil {
		t.Fatal(err)
	}
	firstProof := solveRegistrationProofForStateTest(firstChallenge, "first-user", "")
	if err := proofState.verifyAndConsume(now, firstProof, "first-user", ""); err != nil {
		t.Fatalf("consume first proof: %v", err)
	}

	secondChallenge, err := proofState.issue(now)
	if err != nil {
		t.Fatal(err)
	}
	secondProof := solveRegistrationProofForStateTest(secondChallenge, "second-user", "")

	var logOutput bytes.Buffer
	previousLogWriter := log.Writer()
	log.SetOutput(&logOutput)
	t.Cleanup(func() {
		log.SetOutput(previousLogWriter)
	})

	for attempt := 0; attempt < 2; attempt++ {
		consumeErr := proofState.verifyAndConsume(now, secondProof, "second-user", "")
		if !errors.Is(consumeErr, errRegistrationProofCapacity) {
			t.Fatalf("capacity consume error = %v, want %v", consumeErr, errRegistrationProofCapacity)
		}
	}
	if usedChallengeCount := len(proofState.usedChallenges); usedChallengeCount != 1 {
		t.Fatalf("used challenge count = %d, want 1", usedChallengeCount)
	}
	if saturationLogCount := strings.Count(logOutput.String(), "registration proof replay table saturated"); saturationLogCount != 1 {
		t.Fatalf("saturation log count = %d, want 1; output = %q", saturationLogCount, logOutput.String())
	}
}

func TestRegistrationProofRemovesExpiredEntryBeforeCapacityRejection(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	proofState := newRegistrationProofState(4, time.Minute, 1, now)

	firstChallenge, err := proofState.issue(now)
	if err != nil {
		t.Fatal(err)
	}
	firstProof := solveRegistrationProofForStateTest(firstChallenge, "first-user", "")
	if err := proofState.verifyAndConsume(now, firstProof, "first-user", ""); err != nil {
		t.Fatalf("consume first proof: %v", err)
	}
	for challengeKey := range proofState.usedChallenges {
		proofState.usedChallenges[challengeKey] = now
	}

	secondChallenge, err := proofState.issue(now)
	if err != nil {
		t.Fatal(err)
	}
	secondProof := solveRegistrationProofForStateTest(secondChallenge, "second-user", "")
	if err := proofState.verifyAndConsume(now, secondProof, "second-user", ""); err != nil {
		t.Fatalf("consume proof after expired-entry cleanup: %v", err)
	}
	if usedChallengeCount := len(proofState.usedChallenges); usedChallengeCount != 1 {
		t.Fatalf("used challenge count = %d, want 1", usedChallengeCount)
	}
}

func TestRegisterReturnsServiceUnavailableWhenReplayTableIsAtCapacity(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	authProtector := NewAuthProtector(AuthProtectorConfig{
		RegistrationProofDifficultyBits:    4,
		RegistrationProofValidity:          time.Minute,
		RegistrationProofMaxUsedChallenges: 1,
	})
	authProtector.now = func() time.Time { return now }

	firstChallenge, err := authProtector.registrationProof.issue(now)
	if err != nil {
		t.Fatal(err)
	}
	firstProof := solveRegistrationProofForStateTest(firstChallenge, "first-user", "")
	if err := authProtector.registrationProof.verifyAndConsume(now, firstProof, "first-user", ""); err != nil {
		t.Fatalf("consume first proof: %v", err)
	}

	secondChallenge, err := authProtector.registrationProof.issue(now)
	if err != nil {
		t.Fatal(err)
	}
	requestBody, err := json.Marshal(RegisterRequest{
		Username: "second-user",
		Password: "password123",
		Proof:    solveRegistrationProofForStateTest(secondChallenge, "second-user", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{
		Store: store.TestStore{},
		InitialServerSettings: config.ServerSettings{
			RegistrationMode: store.RegistrationModeFree,
		},
		AuthProtector: authProtector,
	}
	request := httptest.NewRequest(http.MethodPost, "/panel/v1/auth/register", bytes.NewReader(requestBody))
	responseRecorder := httptest.NewRecorder()

	handler.register(responseRecorder, request)

	if responseRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf(
			"status = %d, want %d; body = %s",
			responseRecorder.Code,
			http.StatusServiceUnavailable,
			responseRecorder.Body.String(),
		)
	}
	var responseBody errorResponse
	if err := json.NewDecoder(responseRecorder.Body).Decode(&responseBody); err != nil {
		t.Fatal(err)
	}
	if responseBody.Code != "unavailable" {
		t.Fatalf("error code = %q, want %q", responseBody.Code, "unavailable")
	}
	if responseBody.Error != "registration temporarily unavailable" {
		t.Fatalf(
			"error message = %q, want %q",
			responseBody.Error,
			"registration temporarily unavailable",
		)
	}
}

func solveRegistrationProofForStateTest(challenge RegistrationChallengeResponse, username, inviteCode string) RegistrationProof {
	for nonce := uint64(0); ; nonce++ {
		digest := calculateRegistrationProofDigest(challenge.Challenge, username, inviteCode, nonce)
		if hasLeadingZeroBits(digest, challenge.Difficulty) {
			return RegistrationProof{
				Challenge: challenge.Challenge,
				Nonce:     strconv.FormatUint(nonce, 10),
			}
		}
	}
}

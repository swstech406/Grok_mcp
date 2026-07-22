package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/MapleMapleCat/Grok_Search_Mcp/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const (
	bootstrapPasswordLength       = 12
	maximumBootstrapFileBytes     = 4096
	minimumBootstrapPasswordBytes = 8
	maximumBootstrapPasswordBytes = 72
)

// BootstrapAdminCredentials is the restricted file format used to hand the
// initial administrator credential to the operator.
type BootstrapAdminCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// EnsureBootstrapAdmin creates or recovers a bootstrap administrator from a
// restrictive credential file when the database has no enabled administrator.
func EnsureBootstrapAdmin(
	ctx context.Context,
	storeInstance store.Store,
	credentialPath string,
) (*BootstrapAdminCredentials, error) {
	enabledAdminCount, err := storeInstance.CountEnabledAdmins(ctx)
	if err != nil {
		return nil, fmt.Errorf("count enabled admins: %w", err)
	}
	if enabledAdminCount > 0 {
		return nil, nil
	}

	credentials, err := loadOrCreateBootstrapCredentials(credentialPath)
	if err != nil {
		return nil, err
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(credentials.Password), 12)
	if err != nil {
		return nil, fmt.Errorf("hash bootstrap password: %w", err)
	}

	if _, err := storeInstance.CreateUser(ctx, credentials.Username, string(passwordHash), store.RoleAdmin); err != nil {
		if errors.Is(err, store.ErrUsernameTaken) {
			return promoteExistingBootstrapAdmin(ctx, storeInstance, string(passwordHash), credentials)
		}
		return nil, fmt.Errorf("create admin user: %w", err)
	}

	return credentials, nil
}

func logBootstrapCredentialsAvailable(credentialPath string) {
	log.Printf("bootstrap administrator credentials available at %s", credentialPath)
}

func promoteExistingBootstrapAdmin(
	ctx context.Context,
	storeInstance store.Store,
	passwordHash string,
	credentials *BootstrapAdminCredentials,
) (*BootstrapAdminCredentials, error) {
	existingUser, err := storeInstance.GetUserByUsername(ctx, bootstrapAdminUsername)
	if err != nil {
		return nil, fmt.Errorf("lookup existing admin user: %w", err)
	}
	if existingUser == nil {
		return nil, fmt.Errorf("username taken but user not found")
	}
	enabled := true
	adminRole := store.RoleAdmin
	revokeTokens := true
	if _, err := storeInstance.UpdateUser(ctx, existingUser.ID, store.UserUpdates{
		Enabled:      &enabled,
		Role:         &adminRole,
		PasswordHash: &passwordHash,
		RevokeTokens: &revokeTokens,
	}); err != nil {
		return nil, fmt.Errorf("promote existing admin: %w", err)
	}
	return credentials, nil
}

func loadOrCreateBootstrapCredentials(credentialPath string) (*BootstrapAdminCredentials, error) {
	credentialPath = strings.TrimSpace(credentialPath)
	if credentialPath == "" {
		return nil, fmt.Errorf("bootstrap credential path is required")
	}

	credentialFile, err := os.OpenFile(credentialPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		return writeNewBootstrapCredentials(credentialPath, credentialFile)
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("exclusively create bootstrap credential file: %w", err)
	}
	return readExistingBootstrapCredentials(credentialPath)
}

func writeNewBootstrapCredentials(
	credentialPath string,
	credentialFile *os.File,
) (_ *BootstrapAdminCredentials, returnErr error) {
	fileClosed := false
	defer func() {
		if !fileClosed {
			_ = credentialFile.Close()
		}
		if returnErr != nil {
			_ = os.Remove(credentialPath)
		}
	}()

	password, err := randomBootstrapPassword(bootstrapPasswordLength)
	if err != nil {
		return nil, fmt.Errorf("generate bootstrap password: %w", err)
	}
	credentials := &BootstrapAdminCredentials{
		Username: bootstrapAdminUsername,
		Password: password,
	}
	credentialJSON, err := json.Marshal(credentials)
	if err != nil {
		return nil, fmt.Errorf("encode bootstrap credentials: %w", err)
	}
	if len(credentialJSON) > maximumBootstrapFileBytes {
		return nil, fmt.Errorf("encoded bootstrap credentials exceed size limit")
	}
	if err := credentialFile.Chmod(0o600); err != nil {
		return nil, fmt.Errorf("set bootstrap credential permissions: %w", err)
	}
	if err := writeAll(credentialFile, credentialJSON); err != nil {
		return nil, fmt.Errorf("write bootstrap credential file: %w", err)
	}
	if err := credentialFile.Sync(); err != nil {
		return nil, fmt.Errorf("sync bootstrap credential file: %w", err)
	}
	if err := credentialFile.Close(); err != nil {
		fileClosed = true
		return nil, fmt.Errorf("close bootstrap credential file: %w", err)
	}
	fileClosed = true
	if err := syncParentDirectory(credentialPath); err != nil {
		return nil, err
	}
	if err := verifyBootstrapCredentialPath(credentialPath); err != nil {
		return nil, err
	}
	return credentials, nil
}

func readExistingBootstrapCredentials(credentialPath string) (*BootstrapAdminCredentials, error) {
	pathInfo, err := os.Lstat(credentialPath)
	if err != nil {
		return nil, fmt.Errorf("inspect bootstrap credential file: %w", err)
	}
	if err := validateBootstrapCredentialFileInfo(pathInfo); err != nil {
		return nil, err
	}

	credentialFile, err := openBootstrapCredentialFile(credentialPath)
	if err != nil {
		return nil, fmt.Errorf("open bootstrap credential file: %w", err)
	}
	openedInfo, statErr := credentialFile.Stat()
	if statErr != nil {
		_ = credentialFile.Close()
		return nil, fmt.Errorf("inspect opened bootstrap credential file: %w", statErr)
	}
	if err := validateBootstrapCredentialFileInfo(openedInfo); err != nil {
		_ = credentialFile.Close()
		return nil, err
	}
	if !os.SameFile(pathInfo, openedInfo) {
		_ = credentialFile.Close()
		return nil, fmt.Errorf("bootstrap credential file changed while opening")
	}

	credentialJSON, readErr := io.ReadAll(io.LimitReader(credentialFile, maximumBootstrapFileBytes+1))
	closeErr := credentialFile.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read bootstrap credential file: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close bootstrap credential file: %w", closeErr)
	}
	if len(credentialJSON) > maximumBootstrapFileBytes {
		return nil, fmt.Errorf("bootstrap credential file exceeds %d bytes", maximumBootstrapFileBytes)
	}

	credentials, err := decodeBootstrapCredentials(credentialJSON)
	if err != nil {
		return nil, err
	}
	return credentials, nil
}

func verifyBootstrapCredentialPath(credentialPath string) error {
	pathInfo, err := os.Lstat(credentialPath)
	if err != nil {
		return fmt.Errorf("verify bootstrap credential file: %w", err)
	}
	return validateBootstrapCredentialFileInfo(pathInfo)
}

func validateBootstrapCredentialFileInfo(fileInfo os.FileInfo) error {
	if fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() {
		return fmt.Errorf("bootstrap credential path must be a regular file")
	}
	if permissions := fileInfo.Mode().Perm(); permissions != 0o600 {
		return fmt.Errorf("bootstrap credential file permissions are %#o, require 0600", permissions)
	}
	return nil
}

func decodeBootstrapCredentials(credentialJSON []byte) (*BootstrapAdminCredentials, error) {
	decoder := json.NewDecoder(bytes.NewReader(credentialJSON))
	decoder.DisallowUnknownFields()
	var credentials BootstrapAdminCredentials
	if err := decoder.Decode(&credentials); err != nil {
		return nil, fmt.Errorf("decode bootstrap credential file: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("bootstrap credential file contains trailing JSON data")
	}
	if credentials.Username != bootstrapAdminUsername {
		return nil, fmt.Errorf("bootstrap credential username must be %q", bootstrapAdminUsername)
	}
	passwordLength := len(credentials.Password)
	if passwordLength < minimumBootstrapPasswordBytes || passwordLength > maximumBootstrapPasswordBytes {
		return nil, fmt.Errorf("bootstrap credential password must be 8-72 bytes")
	}
	return &credentials, nil
}

func writeAll(destination io.Writer, content []byte) error {
	for len(content) > 0 {
		writtenBytes, err := destination.Write(content)
		if err != nil {
			return err
		}
		if writtenBytes <= 0 {
			return io.ErrShortWrite
		}
		content = content[writtenBytes:]
	}
	return nil
}

func syncParentDirectory(path string) error {
	parentDirectory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open bootstrap credential directory: %w", err)
	}
	syncErr := parentDirectory.Sync()
	closeErr := parentDirectory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync bootstrap credential directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close bootstrap credential directory: %w", closeErr)
	}
	return nil
}

func randomBootstrapPassword(length int) (string, error) {
	const passwordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	if length <= 0 {
		return "", fmt.Errorf("password length must be positive")
	}

	passwordBytes := make([]byte, length)
	maximumIndex := big.NewInt(int64(len(passwordAlphabet)))
	for passwordIndex := range passwordBytes {
		randomIndex, err := rand.Int(rand.Reader, maximumIndex)
		if err != nil {
			return "", err
		}
		passwordBytes[passwordIndex] = passwordAlphabet[randomIndex.Int64()]
	}
	return string(passwordBytes), nil
}

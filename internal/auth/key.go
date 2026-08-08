package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
)

const generatedKeyPrefix = "c4a_"

type KeyManager struct {
	apiKey    string
	generated bool
}

func NewKeyManager(configuredKey string, allowGeneration bool) (*KeyManager, error) {
	if configuredKey != "" {
		return &KeyManager{
			apiKey:    configuredKey,
			generated: false,
		}, nil
	}
	if !allowGeneration {
		return nil, fmt.Errorf("api key is required when generation is disabled")
	}

	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return nil, fmt.Errorf("generate secure api key: %w", err)
	}

	return &KeyManager{
		apiKey:    generatedKeyPrefix + hex.EncodeToString(randomBytes),
		generated: true,
	}, nil
}

func (m *KeyManager) APIKey() string {
	return m.apiKey
}

func (m *KeyManager) Generated() bool {
	return m.generated
}

func (m *KeyManager) IsAuthorized(authHeader string) bool {
	token, ok := parseBearer(authHeader)
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(m.apiKey)) == 1
}

func parseBearer(authHeader string) (string, bool) {
	if authHeader == "" {
		return "", false
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 {
		return "", false
	}
	if !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	if strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	return strings.TrimSpace(parts[1]), true
}

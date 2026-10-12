package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
)

type PasswordHasherOptions struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
	Random      io.Reader
}

type PasswordHasher struct {
	options PasswordHasherOptions
}

func NewPasswordHasher(options PasswordHasherOptions) (*PasswordHasher, error) {
	if options.MemoryKiB == 0 && options.Iterations == 0 && options.Parallelism == 0 &&
		options.SaltLength == 0 && options.KeyLength == 0 {
		options.MemoryKiB = 19 * 1024
		options.Iterations = 2
		options.Parallelism = 1
		options.SaltLength = 16
		options.KeyLength = 32
	}
	if options.MemoryKiB < 8*uint32(options.Parallelism) || options.MemoryKiB > 64*1024 ||
		options.Iterations < 1 || options.Iterations > 10 ||
		options.Parallelism < 1 || options.Parallelism > 8 ||
		options.SaltLength < 16 || options.SaltLength > 64 ||
		options.KeyLength < 16 || options.KeyLength > 64 {
		return nil, errors.New("Argon2id password parameters are invalid")
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	return &PasswordHasher{options: options}, nil
}

func (h *PasswordHasher) Hash(password string) (string, error) {
	if h == nil {
		return "", errors.New("password hasher is not initialized")
	}
	salt := make([]byte, h.options.SaltLength)
	if _, err := io.ReadFull(h.options.Random, salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey(
		[]byte(password),
		salt,
		h.options.Iterations,
		h.options.MemoryKiB,
		h.options.Parallelism,
		h.options.KeyLength,
	)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		h.options.MemoryKiB,
		h.options.Iterations,
		h.options.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func (h *PasswordHasher) Verify(encodedHash, password string) bool {
	if h == nil || encodedHash == "" {
		return false
	}
	parameters, salt, expected, err := parseArgon2idHash(encodedHash)
	if err != nil {
		return false
	}
	actual := argon2.IDKey(
		[]byte(password),
		salt,
		parameters.Iterations,
		parameters.MemoryKiB,
		parameters.Parallelism,
		uint32(len(expected)),
	)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func parseArgon2idHash(encoded string) (PasswordHasherOptions, []byte, []byte, error) {
	if len(encoded) > 512 {
		return PasswordHasherOptions{}, nil, nil, errors.New("Argon2id hash is too large")
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" ||
		parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return PasswordHasherOptions{}, nil, nil, errors.New("invalid Argon2id hash")
	}
	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil ||
		parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", memory, iterations, parallelism) {
		return PasswordHasherOptions{}, nil, nil, errors.New("invalid Argon2id parameters")
	}
	minimumEncodedLength := base64.RawStdEncoding.EncodedLen(16)
	maximumEncodedLength := base64.RawStdEncoding.EncodedLen(64)
	if len(parts[4]) < minimumEncodedLength || len(parts[4]) > maximumEncodedLength ||
		len(parts[5]) < minimumEncodedLength || len(parts[5]) > maximumEncodedLength {
		return PasswordHasherOptions{}, nil, nil, errors.New("Argon2id salt or key length is invalid")
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return PasswordHasherOptions{}, nil, nil, errors.New("invalid Argon2id salt")
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return PasswordHasherOptions{}, nil, nil, errors.New("invalid Argon2id key")
	}
	parameters := PasswordHasherOptions{
		MemoryKiB:   memory,
		Iterations:  iterations,
		Parallelism: parallelism,
		SaltLength:  uint32(len(salt)),
		KeyLength:   uint32(len(key)),
	}
	if _, err := NewPasswordHasher(parameters); err != nil {
		return PasswordHasherOptions{}, nil, nil, err
	}
	return parameters, salt, key, nil
}

package social

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

type vault struct{ aead cipher.AEAD }

func newVault(key []byte) (*vault, error) {
	if len(key) != 32 {
		return nil, errors.New("social encryption key must contain 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &vault{aead: aead}, nil
}

func (v *vault) seal(plain []byte, owner string) ([]byte, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return v.aead.Seal(append([]byte{1}, nonce...), nonce, plain, []byte(owner)), nil
}

func (v *vault) open(encoded []byte, owner string) ([]byte, error) {
	if len(encoded) < 1+v.aead.NonceSize()+v.aead.Overhead() || encoded[0] != 1 {
		return nil, errors.New("invalid encrypted social credential")
	}
	return v.aead.Open(nil, encoded[1:1+v.aead.NonceSize()], encoded[1+v.aead.NonceSize():], []byte(owner))
}

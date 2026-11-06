package config

import (
	"crypto/rand"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
)

const passcodeAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

const passcodeLength = 10

func LoadPasscode(dir string) (string, error) {
	path := filepath.Join(dir, "passcode")
	data, err := os.ReadFile(path)
	if err == nil {
		passcode := strings.TrimSpace(string(data))
		if passcode == "" {
			return "", errors.New(path + " is empty")
		}
		return passcode, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	chars := make([]byte, passcodeLength)
	for i := range chars {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(passcodeAlphabet))))
		if err != nil {
			return "", err
		}
		chars[i] = passcodeAlphabet[n.Int64()]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(append(chars, '\n')); err != nil {
		return "", err
	}
	return string(chars), nil
}

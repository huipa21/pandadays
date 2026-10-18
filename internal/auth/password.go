// Package auth provides account, password, and session services.
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const PasswordCost = 12

var ErrPasswordPolicy = errors.New("password must be valid UTF-8, 8-32 characters, and at most 72 bytes")

func ValidatePassword(password string) error {
	n := utf8.RuneCountInString(password)
	if !utf8.ValidString(password) || n < 8 || n > 32 || len(password) > 72 {
		return ErrPasswordPolicy
	}
	return nil
}

func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), PasswordCost)
	return string(hash), err
}

func CheckPassword(hash, password string) (bool, error) {
	if ValidatePassword(password) != nil {
		return false, nil
	}
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) || errors.Is(err, bcrypt.ErrPasswordTooLong) {
		return false, nil
	}
	return err == nil, err
}

// RandomPassword returns 16 uniformly random URL-safe characters (96 bits).
func RandomPassword() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

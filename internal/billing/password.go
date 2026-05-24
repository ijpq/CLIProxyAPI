package billing

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// ErrInvalidPassword is returned by ComparePassword when the supplied password
// does not match the stored hash.
var ErrInvalidPassword = errors.New("billing: invalid password")

// HashPassword returns a bcrypt hash of the plaintext password using the
// library default cost.
func HashPassword(password string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashed), nil
}

// ComparePassword verifies a plaintext password against a stored bcrypt hash.
// Returns ErrInvalidPassword on mismatch so callers can distinguish auth
// failures from infrastructure errors.
func ComparePassword(hash, password string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return ErrInvalidPassword
		}
		return err
	}
	return nil
}

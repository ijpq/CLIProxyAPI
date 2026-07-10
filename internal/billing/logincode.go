package billing

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrCodeCooldown is returned by LoginCodeStore.Generate when a code was
// requested for the same email within the cooldown window.
var ErrCodeCooldown = errors.New("billing: login code requested too recently")

// LoginCodeStore holds short-lived email login codes in memory. It is fine for
// a single-instance deployment; move it to a shared store (Redis/Postgres) for
// multi-replica setups.
type LoginCodeStore struct {
	ttl      time.Duration
	cooldown time.Duration
	mu       sync.Mutex
	entries  map[string]*loginCodeEntry
}

type loginCodeEntry struct {
	code      string
	expiresAt time.Time
	sentAt    time.Time
	attempts  int
}

// NewLoginCodeStore constructs a store. ttl bounds how long a code is valid;
// cooldown is the minimum spacing between code requests for one email.
func NewLoginCodeStore(ttl, cooldown time.Duration) *LoginCodeStore {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if cooldown <= 0 {
		cooldown = 60 * time.Second
	}
	return &LoginCodeStore{ttl: ttl, cooldown: cooldown, entries: make(map[string]*loginCodeEntry)}
}

// Generate issues a new 6-digit code for email, enforcing the per-email
// cooldown. Returns ErrCodeCooldown if one was issued too recently.
func (s *LoginCodeStore) Generate(email string) (string, error) {
	email = normalizeEmail(email)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[email]; ok && now.Sub(e.sentAt) < s.cooldown {
		return "", ErrCodeCooldown
	}
	code := randomCode()
	s.entries[email] = &loginCodeEntry{code: code, expiresAt: now.Add(s.ttl), sentAt: now}
	return code, nil
}

// Verify checks code for email. On success the entry is consumed (single-use).
// Attempts are capped to prevent brute force.
func (s *LoginCodeStore) Verify(email, code string) bool {
	email = normalizeEmail(email)
	code = strings.TrimSpace(code)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[email]
	if !ok || now.After(e.expiresAt) {
		delete(s.entries, email)
		return false
	}
	e.attempts++
	if e.attempts > 5 {
		delete(s.entries, email)
		return false
	}
	if code != "" && subtle.ConstantTimeCompare([]byte(e.code), []byte(code)) == 1 {
		delete(s.entries, email)
		return true
	}
	return false
}

func randomCode() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Extremely unlikely; fall back to a time-derived value.
		return fmt.Sprintf("%06d", time.Now().UnixNano()%1000000)
	}
	n := (uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])) % 1000000
	return fmt.Sprintf("%06d", n)
}

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

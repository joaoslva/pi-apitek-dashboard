package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// OWASP's argon2id baseline. Each hash holds 19 MiB, so at most two run at
// once: a burst of logins must not push the Pi into swap.
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32

	minPasswordRunes = 10
	maxPasswordBytes = 256
)

var hashSlots = make(chan struct{}, 2)

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

func validUsername(name string) error {
	if !usernamePattern.MatchString(name) {
		return errors.New("username must be 1-32 of a-z 0-9 . _ -, starting with a letter or digit")
	}
	return nil
}

func validPassword(pw string) error {
	if len(pw) > maxPasswordBytes {
		return fmt.Errorf("password is longer than %d bytes", maxPasswordBytes)
	}
	if utf8.RuneCountInString(pw) < minPasswordRunes {
		return fmt.Errorf("password needs at least %d characters", minPasswordRunes)
	}
	return nil
}

func argonKey(pw, salt []byte, t, m uint32, p uint8, keyLen uint32) []byte {
	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()
	return argon2.IDKey(pw, salt, t, m, p, keyLen)
}

func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argonKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

func verifyPassword(pw, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	// A stored hash with huge parameters would exhaust the Pi on every login.
	if m == 0 || m > 256*1024 || t == 0 || t > 10 || p == 0 || p > 4 {
		return false
	}
	b64 := base64.RawStdEncoding
	salt, err1 := b64.DecodeString(parts[4])
	want, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) < 16 || len(want) > 64 {
		return false
	}
	got := argonKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func newToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func tokenHash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// limiter counts failures per key in fixed windows.
type limiter struct {
	mu     sync.Mutex
	limit  int
	period time.Duration
	fails  map[string]*window
}

type window struct {
	start time.Time
	n     int
}

func newLimiter(limit int, period time.Duration) *limiter {
	return &limiter{limit: limit, period: period, fails: map[string]*window{}}
}

func (l *limiter) blocked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.fails[key]
	return w != nil && now.Sub(w.start) < l.period && w.n >= l.limit
}

func (l *limiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w := l.fails[key]; w != nil && now.Sub(w.start) < l.period {
		w.n++
		return
	}
	l.fails[key] = &window{start: now, n: 1}
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}

func (l *limiter) prune(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, w := range l.fails {
		if now.Sub(w.start) >= l.period {
			delete(l.fails, k)
		}
	}
}

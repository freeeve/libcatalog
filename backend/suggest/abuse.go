package suggest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Abuse pseudonymizes source IPs and issues the anonymous challenge tokens
// that gate patron submissions -- qllpoc's IPHasher, ported intact. The API
// stores HMAC(secret, ip), never the address itself.
type Abuse struct {
	secret []byte
	now    func() time.Time
}

// Challenge age bounds: the floor filters scripted submissions that POST
// faster than a human could pick a vocabulary term; the ceiling expires
// stale dialogs.
const (
	ChallengeMinAge = 3 * time.Second
	ChallengeMaxAge = 2 * time.Hour
)

// NewAbuse requires a non-trivial secret (32 random bytes in deployment
// secret storage).
func NewAbuse(secret []byte) (*Abuse, error) {
	if len(secret) < 16 {
		return nil, errors.New("suggest: abuse secret too short")
	}
	return &Abuse{secret: secret, now: time.Now}, nil
}

// SetClock overrides the clock (tests).
func (a *Abuse) SetClock(now func() time.Time) { a.now = now }

// HashIP returns a hex HMAC-SHA256 of the source IP.
func (a *Abuse) HashIP(ip string) string {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(ip))
	return hex.EncodeToString(mac.Sum(nil))
}

// Challenge issues an anonymous, short-lived proof-of-page-open token:
// "<unix-ts>.<hex hmac>". The patron client fetches one when the suggest
// dialog opens and echoes it on submit. No identity is involved -- the token
// binds only a timestamp.
func (a *Abuse) Challenge() string {
	ts := fmt.Sprintf("%d", a.now().Unix())
	return ts + "." + a.challengeMAC(ts)
}

// VerifyChallenge checks the token's MAC and that its age is within bounds.
func (a *Abuse) VerifyChallenge(token string) bool {
	ts, mac, ok := strings.Cut(token, ".")
	if !ok || !hmac.Equal([]byte(mac), []byte(a.challengeMAC(ts))) {
		return false
	}
	var unix int64
	if _, err := fmt.Sscanf(ts, "%d", &unix); err != nil {
		return false
	}
	age := a.now().Sub(time.Unix(unix, 0))
	return age >= ChallengeMinAge && age <= ChallengeMaxAge
}

func (a *Abuse) challengeMAC(ts string) string {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte("challenge:" + ts))
	return hex.EncodeToString(mac.Sum(nil))
}

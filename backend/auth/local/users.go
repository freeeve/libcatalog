package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/store"
)

// ErrUserExists reports a create against an existing email.
var ErrUserExists = errors.New("local: user exists")

// ErrUserNotFound reports an operation on an unknown email.
var ErrUserNotFound = errors.New("local: user not found")

// user is the stored user document (USER#<email> / PROFILE).
type user struct {
	Email        string      `json:"email"`
	Name         string      `json:"name,omitempty"`
	Roles        []auth.Role `json:"roles"`
	PasswordHash string      `json:"passwordHash"`
	CreatedAt    time.Time   `json:"createdAt"`
}

// UserInfo is the admin-facing view of a user (no credentials).
type UserInfo struct {
	Email     string      `json:"email"`
	Name      string      `json:"name,omitempty"`
	Roles     []auth.Role `json:"roles"`
	CreatedAt time.Time   `json:"createdAt"`
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func userKey(email string) store.Key {
	return store.Key{PK: "USER#" + email, SK: "PROFILE"}
}

// usersIndexKey mirrors each user into one partition so ListUsers is a
// single-partition query (the store has no scans by design).
func usersIndexKey(email string) store.Key {
	return store.Key{PK: "USERS", SK: "EMAIL#" + email}
}

func (s *Service) getUser(ctx context.Context, email string) (user, error) {
	rec, err := s.store.Get(ctx, userKey(email))
	if err != nil {
		return user{}, ErrUserNotFound
	}
	var u user
	if err := json.Unmarshal(rec.Data, &u); err != nil {
		return user{}, fmt.Errorf("local: corrupt user record: %w", err)
	}
	return u, nil
}

// CreateUser adds a user with the given roles.
func (s *Service) CreateUser(ctx context.Context, email, name, password string, roles []auth.Role) error {
	email = normalizeEmail(email)
	if email == "" || !strings.Contains(email, "@") {
		return errors.New("local: invalid email")
	}
	if len(password) < 8 {
		return errors.New("local: password too short (min 8)")
	}
	for _, r := range roles {
		if !r.Valid() {
			return fmt.Errorf("local: unknown role %q", r)
		}
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	u := user{Email: email, Name: name, Roles: roles, PasswordHash: hash, CreatedAt: s.now().UTC()}
	data, err := json.Marshal(u)
	if err != nil {
		return err
	}
	if _, err := s.store.Put(ctx, store.Record{Key: userKey(email), Data: data}, store.CondIfAbsent); err != nil {
		if errors.Is(err, store.ErrConditionFailed) {
			// The profile exists; re-assert its index item anyway so a
			// create that died between the two writes -- which left the
			// user invisible to ListUsers -- is repaired by any retry,
			// including a boot-time Bootstrap re-run (tasks/105).
			_, _ = s.store.Put(ctx, store.Record{Key: usersIndexKey(email)}, store.CondNone)
			return ErrUserExists
		}
		return err
	}
	_, err = s.store.Put(ctx, store.Record{Key: usersIndexKey(email)}, store.CondNone)
	return err
}

// SetRoles replaces a user's roles.
func (s *Service) SetRoles(ctx context.Context, email string, roles []auth.Role) error {
	for _, r := range roles {
		if !r.Valid() {
			return fmt.Errorf("local: unknown role %q", r)
		}
	}
	return s.updateUser(ctx, normalizeEmail(email), func(u *user) { u.Roles = roles })
}

// SetPassword replaces a user's password.
func (s *Service) SetPassword(ctx context.Context, email, password string) error {
	if len(password) < 8 {
		return errors.New("local: password too short (min 8)")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	return s.updateUser(ctx, normalizeEmail(email), func(u *user) { u.PasswordHash = hash })
}

// updateUser applies mutate under optimistic concurrency.
func (s *Service) updateUser(ctx context.Context, email string, mutate func(*user)) error {
	for range 3 {
		rec, err := s.store.Get(ctx, userKey(email))
		if err != nil {
			return ErrUserNotFound
		}
		var u user
		if err := json.Unmarshal(rec.Data, &u); err != nil {
			return err
		}
		mutate(&u)
		data, err := json.Marshal(u)
		if err != nil {
			return err
		}
		rec.Data = data
		if _, err := s.store.Put(ctx, rec, store.CondIfVersion); err == nil {
			return nil
		} else if !errors.Is(err, store.ErrConditionFailed) {
			return err
		}
	}
	return errors.New("local: concurrent update conflict")
}

// DeleteUser removes a user (their refresh tokens die at next use).
func (s *Service) DeleteUser(ctx context.Context, email string) error {
	email = normalizeEmail(email)
	if err := s.store.Delete(ctx, store.Record{Key: userKey(email)}, store.CondNone); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrUserNotFound
		}
		return err
	}
	_ = s.store.Delete(ctx, store.Record{Key: usersIndexKey(email)}, store.CondNone)
	return nil
}

// ListUsers returns all users, email-ordered.
func (s *Service) ListUsers(ctx context.Context) ([]UserInfo, error) {
	var out []UserInfo
	for rec, err := range s.store.Query(ctx, "USERS", "EMAIL#", store.QueryOpt{}) {
		if err != nil {
			return nil, err
		}
		email := strings.TrimPrefix(rec.Key.SK, "EMAIL#")
		u, err := s.getUser(ctx, email)
		if err != nil {
			continue // index item outlived the profile; repairable
		}
		out = append(out, UserInfo{Email: u.Email, Name: u.Name, Roles: u.Roles, CreatedAt: u.CreatedAt})
	}
	return out, nil
}

// Bootstrap ensures a first admin exists, from a "email:password" spec
// (LCATD_BOOTSTRAP_ADMIN). It is a no-op when the user already exists, so it
// is safe to run on every boot.
func (s *Service) Bootstrap(ctx context.Context, spec string) error {
	if spec == "" {
		return nil
	}
	email, password, ok := strings.Cut(spec, ":")
	if !ok {
		return errors.New("local: bootstrap spec must be email:password")
	}
	err := s.CreateUser(ctx, email, "", password, []auth.Role{auth.RoleAdmin})
	if errors.Is(err, ErrUserExists) {
		return nil
	}
	return err
}

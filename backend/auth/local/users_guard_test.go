package local

import (
	"errors"
	"testing"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/store"
)

// TestLastAdminGuard covers the deployment's only admin can be
// neither demoted nor deleted, a second admin unlocks both, and Bootstrap
// re-grants admin to an existing demoted user instead of no-oping.
func TestLastAdminGuard(t *testing.T) {
	svc, _ := newService(t)
	ctx := t.Context()
	if err := svc.CreateUser(ctx, "root@example.org", "", "password123", []auth.Role{auth.RoleAdmin}); err != nil {
		t.Fatal(err)
	}

	// Sole admin: demotion and deletion refuse.
	if err := svc.SetRoles(ctx, "root@example.org", []auth.Role{auth.RoleLibrarian}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demote sole admin err = %v, want ErrLastAdmin", err)
	}
	if err := svc.DeleteUser(ctx, "root@example.org"); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("delete sole admin err = %v, want ErrLastAdmin", err)
	}

	// A second admin unlocks both paths.
	if err := svc.CreateUser(ctx, "second@example.org", "", "password123", []auth.Role{auth.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetRoles(ctx, "root@example.org", []auth.Role{auth.RoleLibrarian}); err != nil {
		t.Fatalf("demote with second admin: %v", err)
	}
	// Now second@ is the sole admin again; guard re-arms.
	if err := svc.DeleteUser(ctx, "second@example.org"); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("delete new sole admin err = %v, want ErrLastAdmin", err)
	}
	// Non-admins delete freely.
	if err := svc.DeleteUser(ctx, "root@example.org"); err != nil {
		t.Fatalf("delete non-admin: %v", err)
	}
}

// TestBootstrapRestoresDemotedAdmin covers 's recovery hatch: an
// existing bootstrap user lacking admin gets it back, loudly signaled.
func TestBootstrapRestoresDemotedAdmin(t *testing.T) {
	svc, st := newService(t)
	ctx := t.Context()
	if restored, err := svc.Bootstrap(ctx, "root@example.org:password123"); err != nil || restored {
		t.Fatalf("first bootstrap = %v restored=%v", err, restored)
	}
	// Simulate the pre-guard lockout: demote directly with a second admin
	// present, then remove the second admin's record out-of-band.
	if err := svc.CreateUser(ctx, "temp@example.org", "", "password123", []auth.Role{auth.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetRoles(ctx, "root@example.org", []auth.Role{auth.RoleLibrarian}); err != nil {
		t.Fatal(err)
	}
	// Remove the second admin OUT-OF-BAND (raw store delete): the guard
	// now refuses deleting a sole admin through the service, so the
	// pre-guard lockout state must be simulated underneath it.
	if err := st.Delete(ctx, store.Record{Key: userKey("temp@example.org")}, store.CondNone); err != nil {
		t.Fatal(err)
	}
	// The hatch: bootstrap on an existing, demoted user re-grants admin.
	restored, err := svc.Bootstrap(ctx, "root@example.org:password123")
	if err != nil || !restored {
		t.Fatalf("restore bootstrap = %v restored=%v", err, restored)
	}
	u, err := svc.GetUser(ctx, "root@example.org")
	if err != nil {
		t.Fatal(err)
	}
	has := false
	for _, r := range u.Roles {
		if r == auth.RoleAdmin {
			has = true
		}
	}
	if !has {
		t.Fatalf("admin not restored: %v", u.Roles)
	}
	// Idempotent thereafter.
	if restored, err := svc.Bootstrap(ctx, "root@example.org:password123"); err != nil || restored {
		t.Fatalf("re-bootstrap = %v restored=%v", err, restored)
	}
}

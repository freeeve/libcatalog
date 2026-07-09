package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/auth/sitegate"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sitegate.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

const fullConfig = `
[sitegate]
issuer      = "https://auth.example.org"
client-id   = "mysite"
site-domain = "opac.example.org"
key-pair-id = "KTEST"
site-name   = "My Site"
min-role    = "moderator"
role-claim  = "groups"
jwks-url    = "https://auth.example.org/jwks.json"
scopes      = ["openid", "email"]
path-prefix = "/_gate"
session-ttl = "45m"

[sitegate.role-map]
staff = "librarian"
`

func TestLoadConfigFull(t *testing.T) {
	pemKey := testPEM(t)
	cfg, err := loadConfig(writeConfig(t, fullConfig), pemKey)
	if err != nil {
		t.Fatal(err)
	}
	want := sitegate.Config{
		Issuer:        "https://auth.example.org",
		ClientID:      "mysite",
		SiteDomain:    "opac.example.org",
		KeyPairID:     "KTEST",
		PrivateKeyPEM: pemKey,
		MinRole:       auth.RoleModerator,
		RoleClaim:     "groups",
		RoleMap:       map[string]auth.Role{"staff": auth.RoleLibrarian},
		JWKSURL:       "https://auth.example.org/jwks.json",
		Scopes:        []string{"openid", "email"},
		SiteName:      "My Site",
		PathPrefix:    "/_gate",
		SessionTTL:    45 * time.Minute,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("loadConfig = %+v\nwant %+v", cfg, want)
	}
	// The loaded config must construct a gate (jwks-url set, so no
	// discovery round trip happens here).
	if _, err := sitegate.New(context.Background(), cfg); err != nil {
		t.Errorf("sitegate.New rejected the loaded config: %v", err)
	}
}

func TestLoadConfigMinimal(t *testing.T) {
	cfg, err := loadConfig(writeConfig(t, `
[sitegate]
issuer      = "https://auth.example.org"
client-id   = "mysite"
site-domain = "opac.example.org"
key-pair-id = "KTEST"
`), testPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	// Optional fields stay zero so sitegate.New applies its defaults.
	if cfg.MinRole != "" || cfg.SessionTTL != 0 || cfg.PathPrefix != "" || cfg.Scopes != nil {
		t.Errorf("optional fields not zero: %+v", cfg)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	for name, body := range map[string]string{
		"bad toml":         `[sitegate` + "\n",
		"bad session-ttl":  "[sitegate]\nsession-ttl = \"12 parsecs\"\n",
		"bad role-map":     "[sitegate]\n[sitegate.role-map]\nstaff = \"emperor\"\n",
		"wrong value type": "[sitegate]\nscopes = \"openid\"\n",
	} {
		if _, err := loadConfig(writeConfig(t, body), ""); err == nil {
			t.Errorf("%s: loadConfig accepted it", name)
		}
	}
	if _, err := loadConfig(filepath.Join(t.TempDir(), "absent.toml"), ""); err == nil {
		t.Error("missing file: loadConfig accepted it")
	}
}

// TestBuildGate exercises the whole main path short of lambda.Start: env
// resolution, config load, gate construction (base64 signer key, jwks-url
// set so no discovery round trip).
func TestBuildGate(t *testing.T) {
	t.Setenv("SITEGATE_CONFIG", writeConfig(t, fullConfig))
	t.Setenv("SITEGATE_PRIVATE_KEY_PEM", base64.StdEncoding.EncodeToString([]byte(testPEM(t))))
	if _, err := buildGate(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SITEGATE_PRIVATE_KEY_PEM", "")
	if _, err := buildGate(); err == nil {
		t.Error("buildGate accepted a missing signer key")
	}
}

func TestDecodeKeyEnv(t *testing.T) {
	pemKey := testPEM(t)
	for name, tc := range map[string]struct{ in, want string }{
		"raw pem":        {pemKey, pemKey},
		"base64 of pem":  {base64.StdEncoding.EncodeToString([]byte(pemKey)), pemKey},
		"padded raw pem": {"  " + pemKey, "  " + pemKey},
		"empty":          {"", ""},
		"garbage":        {"not a key", "not a key"},
	} {
		if got := decodeKeyEnv(tc.in); got != tc.want {
			t.Errorf("%s: decodeKeyEnv = %q, want %q", name, got, tc.want)
		}
	}
	if !strings.HasPrefix(decodeKeyEnv(base64.StdEncoding.EncodeToString([]byte(testPEM(t)))), "-----BEGIN") {
		t.Error("base64 form did not decode to PEM")
	}
}

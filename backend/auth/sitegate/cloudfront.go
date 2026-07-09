package sitegate

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // CloudFront signed cookies require RSA-SHA1.
	"encoding/base64"
	"fmt"
	"net/http"
	"time"
)

// signedCookies mints the CloudFront custom-policy cookie triple gating
// every path on the distribution until expiry. The policy grants the whole
// site (Resource https://{SiteDomain}/*); CloudFront verifies the RSA-SHA1
// signature against the distribution's trusted key group, so no per-request
// compute happens gate-side -- revocation is cookie expiry.
func (g *Gate) signedCookies(expires time.Time) []*http.Cookie {
	policy := fmt.Sprintf(
		`{"Statement":[{"Resource":"https://%s/*","Condition":{"DateLessThan":{"AWS:EpochTime":%d}}}]}`,
		g.cfg.SiteDomain, expires.Unix())
	sum := sha1.Sum([]byte(policy)) //nolint:gosec
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.signer, crypto.SHA1, sum[:])
	if err != nil {
		// Unsignable key: fail closed with no cookies; the gate keeps 403ing.
		return nil
	}
	cookie := func(name, value string) *http.Cookie {
		return &http.Cookie{
			Name: name, Value: value, Domain: g.cfg.SiteDomain, Path: "/",
			MaxAge: int(time.Until(expires).Seconds()),
			Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		}
	}
	return []*http.Cookie{
		cookie("CloudFront-Policy", cfEncode([]byte(policy))),
		cookie("CloudFront-Signature", cfEncode(sig)),
		cookie("CloudFront-Key-Pair-Id", g.cfg.KeyPairID),
	}
}

// cfEncode is CloudFront's base64 variant: standard base64 with the
// URL/cookie-unsafe characters swapped (+ -> -, = -> _, / -> ~).
func cfEncode(b []byte) string {
	s := base64.StdEncoding.EncodeToString(b)
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '+':
			out[i] = '-'
		case '=':
			out[i] = '_'
		case '/':
			out[i] = '~'
		default:
			out[i] = s[i]
		}
	}
	return string(out)
}

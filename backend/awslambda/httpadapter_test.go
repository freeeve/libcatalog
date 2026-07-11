package awslambda

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

func event(method, path, query, body string) events.APIGatewayV2HTTPRequest {
	ev := events.APIGatewayV2HTTPRequest{
		RawPath:        path,
		RawQueryString: query,
		Body:           body,
		Headers:        map[string]string{},
	}
	ev.RequestContext.HTTP.Method = method
	ev.RequestContext.HTTP.SourceIP = "192.0.2.1"
	ev.RequestContext.DomainName = "api.example.org"
	return ev
}

func TestRequestMapping(t *testing.T) {
	var got *http.Request
	var gotBody string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	})
	ev := event(http.MethodPost, "/v1/suggestions", "a=1&b=two", `{"workId":"w1"}`)
	ev.Headers["content-type"] = "application/json"
	ev.Headers["accept"] = "application/json, text/plain"
	ev.Cookies = []string{"s=abc", "t=def"}

	resp, err := Handler(h)(context.Background(), ev)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/v1/suggestions" {
		t.Fatalf("request = %s %s", got.Method, got.URL.Path)
	}
	if got.URL.Query().Get("b") != "two" {
		t.Fatalf("query = %q", got.URL.RawQuery)
	}
	if gotBody != `{"workId":"w1"}` {
		t.Fatalf("body = %q", gotBody)
	}
	if got.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("content-type = %q", got.Header.Get("Content-Type"))
	}
	if len(got.Header.Values("Accept")) != 2 {
		t.Fatalf("accept values = %v, want comma-split into 2", got.Header.Values("Accept"))
	}
	if c, err := got.Cookie("t"); err != nil || c.Value != "def" {
		t.Fatalf("cookie t = %v, %v", c, err)
	}
	if got.RemoteAddr != "192.0.2.1" || got.Host != "api.example.org" {
		t.Fatalf("remote/host = %q / %q", got.RemoteAddr, got.Host)
	}
}

// TestEscapedPathParity covers a percent-escaped path segment must
// reach the mux decoded exactly once, as it does on the standalone server --
// not double-encoded by a url.URL round trip.
func TestEscapedPathParity(t *testing.T) {
	mux := http.NewServeMux()
	var gotEmail string
	mux.HandleFunc("PUT /v1/users/{email}/roles", func(w http.ResponseWriter, r *http.Request) {
		gotEmail = r.PathValue("email")
		w.WriteHeader(http.StatusNoContent)
	})
	ev := event(http.MethodPut, "/v1/users/eve%40example.org/roles", "", "")

	resp, err := Handler(mux)(context.Background(), ev)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d (escaped path failed to route)", resp.StatusCode)
	}
	if gotEmail != "eve@example.org" {
		t.Fatalf("email path value = %q, want %q", gotEmail, "eve@example.org")
	}
}

func TestBase64RequestBody(t *testing.T) {
	raw := []byte{0x00, 0x01, 0xFF}
	ev := event(http.MethodPost, "/v1/x", "", base64.StdEncoding.EncodeToString(raw))
	ev.IsBase64Encoded = true
	var gotBody []byte
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
	})
	if _, err := Handler(h)(context.Background(), ev); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if string(gotBody) != string(raw) {
		t.Fatalf("body = %v, want %v", gotBody, raw)
	}
}

func TestResponseMapping(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		http.SetCookie(w, &http.Cookie{Name: "s", Value: "abc"})
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	resp, err := Handler(h)(context.Background(), event(http.MethodGet, "/v1/x", "", ""))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if resp.StatusCode != http.StatusCreated || resp.Body != `{"ok":true}` || resp.IsBase64Encoded {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Headers["Content-Type"] != "application/json" {
		t.Fatalf("headers = %v", resp.Headers)
	}
	if len(resp.Cookies) != 1 || !strings.HasPrefix(resp.Cookies[0], "s=abc") {
		t.Fatalf("cookies = %v", resp.Cookies)
	}
}

func TestBinaryResponseBase64(t *testing.T) {
	raw := []byte{0x89, 0x50, 0x4E, 0x47, 0x00}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(raw)
	})
	resp, err := Handler(h)(context.Background(), event(http.MethodGet, "/v1/x", "", ""))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !resp.IsBase64Encoded {
		t.Fatal("binary body not base64-flagged")
	}
	decoded, err := base64.StdEncoding.DecodeString(resp.Body)
	if err != nil || string(decoded) != string(raw) {
		t.Fatalf("decoded = %v, %v", decoded, err)
	}
}

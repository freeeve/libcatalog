// Package awslambda adapts the backend's http.Handler to API Gateway v2 HTTP
// events, so cmd/lcatd-lambda serves exactly what cmd/lcatd serves. Only this
// package and its command import the AWS Lambda SDK -- the handler itself
// stays cloud-agnostic.
package awslambda

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"unicode/utf8"

	"github.com/aws/aws-lambda-go/events"
)

// Handler wraps h as an API Gateway v2 HTTP-API event handler for
// lambda.Start.
func Handler(h http.Handler) func(context.Context, events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	return func(ctx context.Context, ev events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
		req, err := httpRequest(ctx, ev)
		if err != nil {
			return events.APIGatewayV2HTTPResponse{StatusCode: http.StatusBadRequest}, nil
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return httpResponse(rec), nil
	}
}

func httpRequest(ctx context.Context, ev events.APIGatewayV2HTTPRequest) (*http.Request, error) {
	body := []byte(ev.Body)
	if ev.IsBase64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(ev.Body)
		if err != nil {
			return nil, fmt.Errorf("awslambda: decode body: %w", err)
		}
		body = decoded
	}
	// Keep the raw request target as-is: routing RawPath through url.URL.Path
	// re-escapes literal '%', so an escaped segment ("/users/eve%40x") would
	// reach PathValue double-encoded under Lambda but decoded on the
	// standalone server.
	target := ev.RawPath
	if target == "" {
		target = "/"
	}
	if ev.RawQueryString != "" {
		target += "?" + ev.RawQueryString
	}
	req, err := http.NewRequestWithContext(ctx, ev.RequestContext.HTTP.Method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range ev.Headers {
		// API GW v2 flattens repeated headers into comma-joined values.
		for part := range strings.SplitSeq(v, ",") {
			req.Header.Add(k, strings.TrimSpace(part))
		}
	}
	if len(ev.Cookies) > 0 {
		req.Header.Set("Cookie", strings.Join(ev.Cookies, "; "))
	}
	req.RemoteAddr = ev.RequestContext.HTTP.SourceIP
	req.Host = ev.RequestContext.DomainName
	return req, nil
}

func httpResponse(rec *httptest.ResponseRecorder) events.APIGatewayV2HTTPResponse {
	resp := events.APIGatewayV2HTTPResponse{
		StatusCode: rec.Code,
		Headers:    map[string]string{},
	}
	for k, vs := range rec.Header() {
		if http.CanonicalHeaderKey(k) == "Set-Cookie" {
			resp.Cookies = append(resp.Cookies, vs...)
			continue
		}
		resp.Headers[k] = strings.Join(vs, ",")
	}
	body := rec.Body.Bytes()
	if utf8.Valid(body) {
		resp.Body = string(body)
	} else {
		resp.Body = base64.StdEncoding.EncodeToString(body)
		resp.IsBase64Encoded = true
	}
	return resp
}

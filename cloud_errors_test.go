package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCloudErrorsDoNotDependOnMessageWords(t *testing.T) {
	for _, tc := range []struct {
		status int
		kind   string
		retry  bool
	}{
		{401, "credentials", false}, {403, "permission", false},
		{404, "source", false}, {429, "rate_limit", true},
		{503, "network", true}, {400, "other", false},
	} {
		err := cloudHTTPError(tc.status, "120", time.Now())
		// Old string matching classifies words like Cookie before rate limiting.
		err.Message = "Cookie 授权 401 限流 429"
		if classifyError(fmt.Errorf("outer: %w", err)) != tc.kind || err.Retryable != tc.retry {
			t.Fatalf("status %d lost structured classification", tc.status)
		}
	}
	if classifyError(nil) != "" || classifyError(context.Canceled) != "cancelled" {
		t.Fatal("nil/cancellation classification")
	}
	if classifyError(errors.New("磁盘空间不足")) != "space" {
		t.Fatal("legacy compatibility lost")
	}
}

func TestCloudRetryAfterBounded(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"120", 2 * time.Minute}, {"-1", 0}, {"999999999999999999", 24 * time.Hour},
		{"invalid", 0}, {now.Add(time.Hour).Format(http.TimeFormat), time.Hour},
		{now.Add(-time.Hour).Format(http.TimeFormat), 0},
	} {
		if got := retryAfter(tc.value, now); got != tc.want {
			t.Fatalf("retry header %q: %v != %v", tc.value, got, tc.want)
		}
	}
	if cloudHTTPError(429, "", now).RetryAfter != 30*time.Second {
		t.Fatal("missing rate-limit backoff")
	}
}

func TestCloudErrorDoesNotExposeUpstreamSecrets(t *testing.T) {
	secret := "never-echo-fixture-secret"
	err := networkError(&url.Error{Op: "Get", URL: "https://example.test/?token=" + secret, Err: errors.New(secret)})
	raw, _ := json.Marshal(err)
	if strings.Contains(err.Error()+string(raw), secret) || classifyError(err) != "network" {
		t.Fatal("network error leaked upstream data")
	}
	err = cloudAPIError(secret + "/?Cookie=secret")
	if strings.Contains(err.Error(), secret) || classifyError(err) != "other" {
		t.Fatal("provider-specific code exposed or misclassified")
	}
	if classifyError(check115(object{"errno": 401})) != "other" {
		t.Fatal("115 provider-specific errno must not be assumed HTTP status")
	}
	if !errors.Is(networkError(context.DeadlineExceeded), context.DeadlineExceeded) {
		t.Fatal("deadline identity lost")
	}
}

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CloudError is the safe, provider-independent contract used by scheduling,
// health and diagnostics. Message must be application-owned text, never an
// upstream response, request URL, credential or download link.
type CloudError struct {
	Kind       string        `json:"kind"`
	Code       string        `json:"code,omitempty"`
	Retryable  bool          `json:"retryable"`
	RetryAfter time.Duration `json:"-"`
	Message    string        `json:"message"`
	cause      error
}

func (e *CloudError) Error() string { return e.Message }
func (e *CloudError) Unwrap() error { return e.cause }

func classifyError(err error) string {
	if err == nil {
		return ""
	}
	var cloud *CloudError
	if errors.As(err, &cloud) {
		return cloud.Kind
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "network"
	}
	// Compatibility for persisted messages and filesystem errors. New provider
	// implementations must return CloudError instead of depending on wording.
	return errorKind(err.Error())
}

func retryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	const maximum = 24 * time.Hour
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds >= int64(maximum/time.Second) {
			return maximum
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return min(maximum, max(time.Duration(0), at.Sub(now)))
	}
	return 0
}

func cloudHTTPError(status int, retryHeader string, now time.Time) *CloudError {
	e := &CloudError{Kind: "other", Code: strconv.Itoa(status), Message: fmt.Sprintf("网盘接口返回 HTTP %d，请检查通道配置或账号权益", status)}
	switch {
	case status == http.StatusUnauthorized:
		e.Kind, e.Message = "credentials", "网盘凭据已失效，请更新凭据后重新验证"
	case status == http.StatusForbidden:
		e.Kind, e.Message = "permission", "网盘拒绝访问，请检查权限、账号权益或通道限制"
	case status == http.StatusNotFound:
		e.Kind, e.Message = "source", "网盘未找到请求的资源，请核对来源目录或文件"
	case status == http.StatusTooManyRequests:
		e.Kind, e.Retryable, e.Message = "rate_limit", true, "网盘请求受限，请等待后重试"
		e.RetryAfter = max(30*time.Second, retryAfter(retryHeader, now))
	case status == http.StatusRequestTimeout || status >= 500 && status <= 599:
		e.Kind, e.Retryable, e.Message = "network", true, "网盘暂时不可用，请稍后重试"
		e.RetryAfter = retryAfter(retryHeader, now)
	}
	return e
}

func cloudProtocolError(message string) error {
	return &CloudError{Kind: "protocol", Code: "invalid_response", Message: message}
}

func cloudAPIError(code string) error {
	// Only documented HTTP-shaped statuses have shared semantics. Do not infer
	// expired credentials from provider-specific numeric codes or hint words.
	if status, err := strconv.Atoi(code); err == nil && (status == 401 || status == 403 || status == 404 || status == 408 || status == 429 || status >= 500 && status <= 599) {
		return cloudHTTPError(status, "", time.Now())
	}
	return &CloudError{Kind: "other", Code: safeCode(code), Message: fmt.Sprintf("网盘接口拒绝请求（%s），请检查通道状态或账号权益", safeCode(code))}
}

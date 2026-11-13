// Package diagnostics writes bounded runtime failure records without exposing
// arbitrary error messages, paths, URLs, or remote content.
package diagnostics

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

type Field struct{ Key, Value string }

var outputMu sync.Mutex
var output io.Writer = os.Stderr

func SetOutput(writer io.Writer) func() {
	outputMu.Lock()
	previous := output
	if writer == nil {
		output = os.Stderr
	} else {
		output = writer
	}
	outputMu.Unlock()
	return func() { outputMu.Lock(); output = previous; outputMu.Unlock() }
}

// Error records explicitly recognized causes and bounded protocol metadata.
// Unknown error messages are never copied.
func Error(operation string, err error, fields ...Field) {
	if err == nil || expectedOnly(err, 0) {
		return
	}
	record := "ERROR operation=" + token(operation)
	var causes []string
	var walk func(error, int)
	walk = func(e error, depth int) {
		if e == nil || depth > 32 || len(causes) >= 32 {
			return
		}
		name := reflect.TypeOf(e).String()
		// These labels are fixed strings; never copy the error's message.
		switch {
		case e == context.DeadlineExceeded:
			name += ": deadline exceeded"
		case e == io.ErrUnexpectedEOF:
			name += ": unexpected EOF"
		}
		for _, label := range []string{"encode agent request", "build agent request", "agent transport", "decode agent stream event", "scan agent response", "Telegram transport", "read Telegram response", "decode Telegram response", "Codex request", "read Codex response"} {
			if strings.HasPrefix(e.Error(), label+":") {
				name += ": " + label
				break
			}
		}
		if number, ok := e.(syscall.Errno); ok {
			name += ": " + number.Error()
		}
		switch cause := e.(type) {
		case *json.SyntaxError:
			name += fmt.Sprintf(": invalid JSON at byte %d", cause.Offset)
		case *json.UnmarshalTypeError:
			name += fmt.Sprintf(": JSON type mismatch at byte %d", cause.Offset)
		case *net.DNSError:
			switch {
			case cause.IsNotFound:
				name += ": DNS name not found"
			case cause.IsTimeout:
				name += ": DNS lookup timed out"
			case cause.IsTemporary:
				name += ": temporary DNS failure"
			default:
				switch cause.Err {
				case "server misbehaving", "no such host", "no suitable address found", "server failure":
					name += ": " + cause.Err
				default:
					name += ": DNS lookup failed"
				}
			}
		case *net.OpError:
			if cause.Timeout() {
				name += ": transport timed out"
			}
		case tls.RecordHeaderError:
			name += ": invalid TLS record header"
		case x509.UnknownAuthorityError:
			name += ": TLS certificate signed by unknown authority"
		case x509.HostnameError:
			name += ": TLS certificate hostname mismatch"
		case x509.CertificateInvalidError:
			name += ": invalid TLS certificate"
		}
		if description := knownDescription(e.Error()); description != "" {
			name += ": " + description
		}
		if detail, ok := e.(interface{ DiagnosticFields() []Field }); ok {
			// These fields use the same validation as caller context below.
			fields = append(fields, detail.DiagnosticFields()...)
		}
		causes = append(causes, name)
		if joined, ok := e.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				walk(child, depth+1)
			}
			return
		}
		walk(errors.Unwrap(e), depth+1)
	}
	walk(err, 0)
	for i, field := range fields {
		if i >= 16 {
			break
		}
		value := ""
		switch field.Key {
		case "status":
			if len(field.Value) >= 3 {
				if n, e := strconv.Atoi(field.Value[:3]); e == nil && n >= 100 && n <= 599 {
					value = field.Value[:3]
				}
			}
		case "request_id":
			value = RequestID(field.Value)
		case "configured_model":
			if len(field.Value) <= 96 && token(field.Value) == field.Value {
				value = field.Value
			}
		case "reason":
			value = knownDescription(field.Value)
		case "backend_message":
			value = RemoteMessage(field.Value)
		case "backend_type":
			value = BackendValue(field.Value)
		case "backend_code":
			value = BackendValue(field.Value)
			if n, e := strconv.Atoi(field.Value); e == nil && n >= 100 && n <= 599 {
				value = strconv.Itoa(n)
			}
		case "phase":
			switch field.Value {
			case "pre-stream", "stream", "post-stream":
				value = field.Value
			}
		case "method":
			switch field.Value {
			case "agent probe", "getUpdates", "setMyCommands", "sendMessage", "editMessageText", "sendDocument", "getFile", "downloadAttachment", "downloadFile", "deleteMessage", "sendChatAction":
				value = field.Value
			}
		}
		if value != "" {
			record += " " + field.Key + "=" + strconv.Quote(value)
		}
	}

	record += " causes=" + strconv.Quote(strings.Join(causes, " | ")) + "\n"
	outputMu.Lock()
	_, _ = io.WriteString(output, record)
	outputMu.Unlock()
}
func token(s string) string {
	if len(s) > 96 {
		s = s[:96]
	}
	var b strings.Builder
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c) {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}
func expectedOnly(err error, depth int) bool {
	if err == nil {
		return true
	}
	if depth > 32 {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !expectedOnly(child, depth+1) {
				return false
			}
		}
		return true
	}
	if child := errors.Unwrap(err); child != nil {
		return expectedOnly(child, depth+1)
	}
	return err == context.Canceled || err == io.EOF
}

// BackendValue accepts known protocol classifications, never arbitrary strings.
func BackendValue(value string) string {
	switch value {
	case "invalid_request_error", "authentication_error", "auth_error", "permission_error", "not_found_error", "rate_limit_error", "server_error", "api_error", "provider_error", "backend_error", "gateway_rejected",
		"invalid_api_key", "invalid_authentication", "token_expired", "invalid_token", "insufficient_quota", "rate_limit_exceeded", "model_not_found", "model_not_available", "context_length_exceeded", "invalid_json", "content_policy_violation", "content_filter", "request_timeout", "timeout", "overloaded", "internal_error", "invalid_grant", "invalid_client", "access_denied", "authorization_pending", "slow_down", "expired_token", "unsupported_country_region_territory":
		return value
	}
	return ""
}

// RequestID retains standard opaque request IDs only, with no URL/control data.
func RequestID(value string) string {
	if len(value) < 8 || len(value) > 80 {
		return ""
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"sk-", "sk_", "bearer", "token", "secret", "password"} {
		if strings.Contains(lower, marker) {
			return ""
		}
	}
	if strings.HasPrefix(value, "req_") || strings.HasPrefix(value, "req-") {
		for _, c := range value {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return ""
			}
		}
		return value
	}
	if len(value) == 36 {
		for i, c := range value {
			if i == 8 || i == 13 || i == 18 || i == 23 {
				if c != '-' {
					return ""
				}
				continue
			}
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return ""
			}
		}
		return value
	}
	return ""
}

func knownDescription(message string) string {
	// Exact matches against application-owned literals. Never emit a suffix or
	// interpolate a value from an arbitrary error string.
	switch message {
	case "Codex backend request failed", "Codex stream failed", "Codex response failed: backend returned an error", "device code request failed", "device authentication failed", "token exchange failed", "saved refresh token was rejected", "token refresh failed", "Telegram rejected message formatting",
		"OpenAI subscription authentication required: credentials were not found", "OpenAI subscription authentication required", "request authentication failed",
		"OpenAI subscription authentication required: credential file has no access token",
		"OpenAI subscription authentication required: saved credentials have no refresh token",
		"OpenAI subscription authentication required: saved access token is expired and has no refresh token",
		"credential path is empty", "credential file must not be a symbolic link", "token response has no access token",
		"authentication checker is unavailable", "chat provider is nil", "too many attachments", "invalid conversation history",
		"Codex response is missing", "Codex response body is missing", "OpenAI response is missing", "OpenAI response body is missing",
		"Codex response failed", "Codex response stream ended before completion", "Codex response contained no output text", "Codex response contained no output",
		"Codex requested a tool in a tool-free request", "request contains multiple JSON values",
		"Telegram client is unavailable", "Telegram response body is missing", "Telegram response contained no update list", "Telegram response body exceeds the limit",
		"remote agent client is unavailable", "agent response body is missing", "agent response contained no final reply",
		"attachment has no Telegram file ID", "attachment exceeds the size limit", "Telegram file metadata is invalid or exceeds the size limit",
		"Telegram file response is missing", "Telegram file response body is missing", "Telegram file download returned an unacceptable response", "Telegram file exceeds the size limit":
		return message
	}
	return ""
}

// RemoteMessage preserves exact common fixed rejection text; messages containing
// interpolated request content are deliberately excluded.
func RemoteMessage(value string) string {
	switch value {
	case "Unauthorized", "Internal server error", "Rate limit exceeded", "Bad Request: chat not found", "Bad Request: message is not modified", "Forbidden: bot was blocked by the user", "Forbidden: bot was kicked from the group chat", "Conflict: terminated by other getUpdates request; make sure that only one bot instance is running", "Conflict: can't use getUpdates method while webhook is active; use deleteWebhook to delete the webhook first":
		return value
	}
	return ""
}

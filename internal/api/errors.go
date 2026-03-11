package api

import "errors"

const (
	ErrCodeInvalidConfig         = "ERR_INVALID_CONFIG"
	ErrCodeConfigLoadFailed      = "ERR_CONFIG_LOAD_FAILED"
	ErrCodeConfigSaveFailed      = "ERR_CONFIG_SAVE_FAILED"
	ErrCodeRuntimeAlreadyRunning = "ERR_RUNTIME_ALREADY_RUNNING"
	ErrCodeRuntimeNotRunning     = "ERR_RUNTIME_NOT_RUNNING"
	ErrCodeRuntimeStartFailed    = "ERR_RUNTIME_START_FAILED"
	ErrCodeRuntimeStopFailed     = "ERR_RUNTIME_STOP_FAILED"
	ErrCodeRuleValidationFailed  = "ERR_RULE_VALIDATION_FAILED"
	ErrCodeUpstreamUnavailable   = "ERR_UPSTREAM_UNAVAILABLE"
	ErrCodeProxyListenFailed     = "ERR_PROXY_LISTEN_FAILED"
	ErrCodeSystemProxyFailed     = "ERR_SYSTEM_PROXY_FAILED"
	ErrCodeTUNUnavailable        = "ERR_TUN_UNAVAILABLE"
	ErrCodePermissionDenied      = "ERR_PERMISSION_DENIED"
	ErrCodePlatformUnsupported   = "ERR_PLATFORM_UNSUPPORTED"
	ErrCodeInternal              = "ERR_INTERNAL"
)

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Cause   error  `json:"-"`
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

func (e *APIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func NewError(code, message string) *APIError {
	return &APIError{Code: code, Message: message}
}

func WrapError(code, message string, cause error) *APIError {
	return &APIError{Code: code, Message: message, Cause: cause}
}

func ErrorCode(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	if err == nil {
		return ""
	}
	return ErrCodeInternal
}

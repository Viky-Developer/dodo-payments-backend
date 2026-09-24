package middleware

// APIError represents the standardized error body.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrorResponse represents the standardized JSON error envelope defined in AGENTS.md.
type ErrorResponse struct {
	Error APIError `json:"error"`
}

// NewErrorResponse creates an ErrorResponse with the given error code and message.
func NewErrorResponse(code, message string) ErrorResponse {
	return ErrorResponse{
		Error: APIError{
			Code:    code,
			Message: message,
		},
	}
}

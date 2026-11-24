package tools

// NewError is the test-only structured tool error constructor kept after W1-1
// removed the unused production factory.
func NewError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// NewBaseRegistry is the test-only P1 registry constructor kept after W1-1
// removed the unused production factory. Production uses NewDesignRegistry.
func NewBaseRegistry(deps Deps) *Registry {
	return NewRegistry(baseTools(deps)...)
}

// NewExtractionCompletionValidator is the test-only extraction validator
// constructor kept after W1-1 removed the unused production factory.
func NewExtractionCompletionValidator(runs ExtractionReader, planItemID int64) *ExtractionCompletionValidator {
	return &ExtractionCompletionValidator{runs: runs, planItemID: planItemID}
}

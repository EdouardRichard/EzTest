package tools

// NewError is the test-only structured tool error constructor kept after W1-1
// removed the unused production factory.
func NewError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// NewP1Registry is the test-only P1 registry constructor kept after W1-1
// removed the unused production factory. Production uses NewP2Registry.
func NewP1Registry(deps Deps) *Registry {
	return NewRegistry(p1Tools(deps)...)
}

// NewExtractionCompletionValidator is the test-only extraction validator
// constructor kept after W1-1 removed the unused production factory.
func NewExtractionCompletionValidator(runs ExtractionReader, planItemID int64) *ExtractionCompletionValidator {
	return &ExtractionCompletionValidator{runs: runs, planItemID: planItemID}
}

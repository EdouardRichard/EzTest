package application

import "errors"

// ErrDefaultCompanyMissing is returned when the seeded default company
// (companies.key = DefaultCompanyKey) does not exist. It signals a broken
// installation rather than a client error, so HTTP handlers map it to 500.
var ErrDefaultCompanyMissing = errors.New("resource: default company is missing")

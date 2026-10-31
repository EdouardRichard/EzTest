// Package storage is the content-object ledger domain. Objects are
// content-addressed by SHA-256; references record who uses an object.
package storage

import "time"

// Object is one immutable content blob in the ledger. RelativePath is the
// filestore-relative location and is never exposed over HTTP.
type Object struct {
	SHA256       string
	Size         int64
	MIME         string
	RelativePath string
	CreatedAt    time.Time
}

// Package visual contains the P5 visual-assertion domain: immutable approved
// baselines, comparison results and the deterministic four-state
// classification. It depends only on the standard library and the shared
// platform package.
package visual

import "errors"

// Domain sentinel errors. Application services map them onto the stable API
// error envelope (404/409/422); infrastructure adapters translate driver errors
// onto them.
var (
	ErrNotFound        = errors.New("visual: not found")
	ErrDuplicate       = errors.New("visual: duplicate")
	ErrStateConflict   = errors.New("visual: state conflict")
	ErrInvalidConfig   = errors.New("visual: invalid config")
	ErrInvalidBaseline = errors.New("visual: invalid baseline")
	ErrConfigChanged   = errors.New("visual: config changed")
	ErrInvalidDiff     = errors.New("visual: invalid diff")
)

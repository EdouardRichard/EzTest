package application

import (
	"errors"
	"fmt"
	"sync"

	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// nextID mints one application id (D29). The generator must be wired: the
// composition root injects the process-wide Snowflake generator so every
// service and instance uses a distinct node id.
func nextID(gen IDGenerator) (int64, error) {
	if gen == nil {
		return 0, errors.New("execution: id generator is not configured")
	}
	id := gen.Next()
	if id <= 0 {
		return 0, fmt.Errorf("execution: id generator returned a non-positive id %d", id)
	}
	return id, nil
}

// defaultIDGenerator is the process-wide fallback Snowflake generator used when
// a BatchService is constructed without an injected IDGenerator. It mirrors
// version.DefaultIDGenerator: a single shared generator prevents two services
// created in the same millisecond from minting the same id. Production wiring
// must inject id.NewSnowflake(cfg.SnowflakeNodeID) instead.
var defaultIDGenerator = sync.OnceValue(func() IDGenerator {
	generator, err := platformid.NewSnowflake(1)
	if err != nil {
		panic(fmt.Sprintf("execution: default snowflake generator: %v", err))
	}
	return generator
})

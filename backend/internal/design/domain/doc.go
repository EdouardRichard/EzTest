// Package design is the P2 case-design bounded-context domain: scenarios,
// logical cases, immutable case versions (steps/assertions/sources), user
// confirmations, design rounds/scopes and the pure validation/hashing rules
// shared by the application services.
//
// The domain depends only on the standard library and platform; it never
// imports pgx, net/http or an LLM client and never writes to the database.
package design

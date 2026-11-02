// Package agentapi exposes the agent context's P1 read-only HTTP surface. The
// server mounts it behind the bearer-auth guard; handlers only parse DTOs and
// map errors (Agent.md §6.2). P1 registers exactly one route:
//
//	GET /api/versions/{id}/team-rooms/{team}?limit=
//
// There is no public write API for the task board or team room in P1.
package agentapi

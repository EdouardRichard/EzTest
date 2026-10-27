#!/usr/bin/env node
// Static validation of deploy/docker-compose.yml.
//
// Docker is intentionally not required on this host: the script only parses the
// YAML document and asserts the deployment contract (services, published ports,
// health checks, named volumes and startup ordering). It never talks to a
// container runtime, so passing it must not be reported as "Compose verified by
// running it".

import { readFileSync } from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"
import { parse } from "yaml"

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const repoRoot = path.resolve(scriptDir, "..", "..")
const composePath = path.join(repoRoot, "deploy", "docker-compose.yml")

const problems = []

function check(condition, message) {
  if (!condition) {
    problems.push(message)
  }
}

function readCompose() {
  try {
    return parse(readFileSync(composePath, "utf8"))
  } catch (error) {
    console.error(`compose validation failed: cannot read or parse ${composePath}`)
    console.error(error instanceof Error ? error.message : String(error))
    process.exit(1)
  }
}

function service(name, services) {
  return services[name] ?? {}
}

function environmentKeys(svc) {
  const environment = svc.environment
  if (Array.isArray(environment)) {
    return environment.map((entry) => String(entry).split("=")[0])
  }
  if (environment && typeof environment === "object") {
    return Object.keys(environment)
  }
  return []
}

function portSpecs(svc) {
  return (svc.ports ?? []).map((entry) => {
    if (typeof entry === "string" || typeof entry === "number") {
      return String(entry)
    }
    if (entry && typeof entry === "object") {
      return `${entry.published ?? ""}:${entry.target ?? ""}`
    }
    return String(entry)
  })
}

function mountTargets(svc) {
  return (svc.volumes ?? []).map((entry) => {
    const text = String(entry)
    const parts = text.split(":")
    return { source: parts[0], target: parts[1] ?? "" }
  })
}

function hasHealthyDependency(svc, target) {
  const dependsOn = svc.depends_on
  if (!dependsOn || typeof dependsOn !== "object") {
    return false
  }
  if (Array.isArray(dependsOn)) {
    return dependsOn.includes(target)
  }
  return dependsOn[target]?.condition === "service_healthy"
}

const document = readCompose()
const services = document?.services ?? {}
const api = service("api", services)
const web = service("web", services)
const postgres = service("postgres", services)

for (const name of ["api", "web", "postgres"]) {
  check(Boolean(services[name]), `services.${name} is missing`)
}

check(portSpecs(api).includes("13333:13333"), "api must publish 13333:13333")
check(portSpecs(web).includes("6868:6868"), "web must publish 6868:6868")
check(portSpecs(postgres).includes("5433:5432"), "postgres must publish 5433:5432")

check(
  String(postgres.image ?? "").startsWith("postgres:17"),
  "postgres must use the postgres:17 image",
)

for (const [name, svc] of Object.entries({ api, web, postgres })) {
  const healthcheck = svc.healthcheck ?? {}
  const test = healthcheck.test
  const hasTest = Array.isArray(test)
    ? test.length > 0
    : typeof test === "string" && test.trim().length > 0
  check(hasTest, `${name} must define a non-empty healthcheck.test`)
  check(Boolean(healthcheck.interval), `${name} healthcheck.interval is missing`)
  check(Boolean(healthcheck.retries), `${name} healthcheck.retries is missing`)
}

const volumeNames = new Set(Object.keys(document?.volumes ?? {}))
check(volumeNames.has("pgdata"), "top-level volume pgdata is missing")
check(volumeNames.has("objects"), "top-level volume objects is missing")

const postgresMounts = mountTargets(postgres)
check(
  postgresMounts.some((mount) => mount.source === "pgdata"),
  "postgres must mount the pgdata volume",
)
const apiMounts = mountTargets(api)
check(
  apiMounts.some((mount) => mount.source === "objects" && mount.target === "/data/objects"),
  "api must mount the objects volume at /data/objects",
)

check(hasHealthyDependency(api, "postgres"), "api must depend_on postgres (service_healthy)")
check(hasHealthyDependency(web, "api"), "web must depend_on api (service_healthy)")

const apiEnvironment = environmentKeys(api)
check(
  apiEnvironment.includes("EZTEST_DATABASE_URL"),
  "api environment must define EZTEST_DATABASE_URL",
)
check(apiEnvironment.includes("EZTEST_WEB_ORIGIN"), "api environment must define EZTEST_WEB_ORIGIN")

check(
  String(api.build?.dockerfile ?? "").includes("Dockerfile.api"),
  "api build must use deploy/Dockerfile.api",
)
check(
  String(web.build?.dockerfile ?? "").includes("Dockerfile.web"),
  "web build must use deploy/Dockerfile.web",
)

if (problems.length > 0) {
  console.error(`compose validation failed (${problems.length} problem(s)): ${composePath}`)
  for (const problem of problems) {
    console.error(` - ${problem}`)
  }
  process.exit(1)
}

console.log(`compose validation passed: ${composePath}`)
console.log(
  "services: api (13333), web (6868), postgres (5433); health checks, volumes and order OK",
)

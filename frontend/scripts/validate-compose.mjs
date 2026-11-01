#!/usr/bin/env node
// Static validation of deploy/docker-compose.yml (P7 multi-replica shape).
//
// Docker is intentionally not required on this host: the script only parses the
// YAML document and asserts the deployment contract (six services, published
// ports, health checks, one shared data volume, two API replicas, startup
// ordering, the internal renderer/executor services, and the Dockerfiles each
// service builds). It never talks to a container runtime, so passing it must not
// be reported as "Compose verified by running it".

import { existsSync, readFileSync } from "node:fs"
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

function healthcheckTest(svc) {
  const test = svc.healthcheck?.test
  if (Array.isArray(test)) {
    return test.map(String).join(" ")
  }
  return typeof test === "string" ? test : ""
}

const document = readCompose()
const services = document?.services ?? {}
const api = service("api", services)
const api2 = service("api2", services)
const web = service("web", services)
const postgres = service("postgres", services)
const executor = service("executor", services)
const renderer = service("renderer", services)

const serviceNames = ["api", "api2", "web", "postgres", "executor", "renderer"]
for (const name of serviceNames) {
  check(Boolean(services[name]), `services.${name} is missing`)
}

// Published ports: only api, web and postgres expose host ports.
check(portSpecs(api).includes("13333:13333"), "api must publish 13333:13333")
check(portSpecs(api2).length === 0, "api2 must not publish any host port")
check(portSpecs(web).includes("6868:6868"), "web must publish 6868:6868")
check(portSpecs(postgres).includes("5433:5432"), "postgres must publish 5433:5432")
check(portSpecs(executor).length === 0, "executor must not publish any host port")
check(portSpecs(renderer).length === 0, "renderer must not publish any host port")
check(
  (executor.expose ?? []).map(String).includes("13336"),
  "executor must expose 13336 to the internal network",
)
check(
  (renderer.expose ?? []).map(String).includes("13337"),
  "renderer must expose 13337 to the internal network",
)

check(
  String(postgres.image ?? "").startsWith("postgres:17"),
  "postgres must use the postgres:17 image",
)

for (const name of serviceNames) {
  const svc = services[name] ?? {}
  const healthcheck = svc.healthcheck ?? {}
  const hasTest = healthcheckTest(svc).trim().length > 0
  check(hasTest, `${name} must define a non-empty healthcheck.test`)
  check(Boolean(healthcheck.interval), `${name} healthcheck.interval is missing`)
  check(Boolean(healthcheck.retries), `${name} healthcheck.retries is missing`)
}

// Both API replicas use the P7 readiness probe, not the P0 liveness path.
for (const [name, svc] of Object.entries({ api, api2 })) {
  check(
    healthcheckTest(svc).includes("/api/health/ready"),
    `${name} healthcheck must target /api/health/ready`,
  )
}

// One shared content volume, mounted at /data by both replicas.
const volumeNames = new Set(Object.keys(document?.volumes ?? {}))
check(volumeNames.has("pgdata"), "top-level volume pgdata is missing")
check(volumeNames.has("data"), "top-level volume data is missing")
for (const legacyVolume of ["objects", "attachments", "evidence", "reports", "exports"]) {
  check(
    !volumeNames.has(legacyVolume),
    `legacy 0.1 volume ${legacyVolume} must be replaced by the shared data volume`,
  )
}

const postgresMounts = mountTargets(postgres)
check(
  postgresMounts.some(
    (mount) => mount.source === "pgdata" && mount.target === "/var/lib/postgresql/data",
  ),
  "postgres must mount the pgdata volume at /var/lib/postgresql/data",
)
for (const [name, svc] of Object.entries({ api, api2 })) {
  check(
    mountTargets(svc).some((mount) => mount.source === "data" && mount.target === "/data"),
    `${name} must mount the shared data volume at /data`,
  )
}

check(hasHealthyDependency(api, "postgres"), "api must depend_on postgres (service_healthy)")
check(hasHealthyDependency(api2, "postgres"), "api2 must depend_on postgres (service_healthy)")
check(hasHealthyDependency(web, "api"), "web must depend_on api (service_healthy)")

// Only the four documented 0.2 startup variables may configure the API.
const apiEnvironment = environmentKeys(api)
for (const requiredKey of [
  "EZTEST_DATABASE_URL",
  "EZTEST_API_PORT",
  "EZTEST_WEB_PORT",
  "EZTEST_EXECUTOR_PORT",
]) {
  check(apiEnvironment.includes(requiredKey), `api environment must define ${requiredKey}`)
}
for (const legacyKey of [
  "EZTEST_HTTP_ADDR",
  "EZTEST_WEB_ORIGIN",
  "EZTEST_STORAGE_DIR",
  "EZTEST_VERSION_ATTACHMENT_DIR",
  "EZTEST_EVIDENCE_DIR",
  "EZTEST_REPORT_DIR",
  "EZTEST_EXPORT_DIR",
  "EZTEST_SNOWFLAKE_NODE_ID",
  "EZTEST_OPENAPI_PATH",
  "EZTEST_ENABLE_TEST_ENDPOINTS",
]) {
  check(
    !apiEnvironment.includes(legacyKey),
    `api must not set the 0.1-only environment key ${legacyKey}`,
  )
}

const executorEnvironment = environmentKeys(executor)
for (const requiredKey of ["EZTEST_EXECUTOR_ADDR", "EZTEST_EXECUTOR_TOKEN"]) {
  check(
    executorEnvironment.includes(requiredKey),
    `executor environment must define ${requiredKey}`,
  )
}
const rendererEnvironment = environmentKeys(renderer)
for (const requiredKey of ["EZTEST_RENDERER_ADDR", "EZTEST_RENDER_TOKEN"]) {
  check(
    rendererEnvironment.includes(requiredKey),
    `renderer environment must define ${requiredKey}`,
  )
}

check(
  String(api.build?.dockerfile ?? "").includes("Dockerfile.api"),
  "api build must use deploy/Dockerfile.api",
)
check(
  String(api2.build?.dockerfile ?? "").includes("Dockerfile.api"),
  "api2 build must use deploy/Dockerfile.api",
)
check(
  String(web.build?.dockerfile ?? "").includes("Dockerfile.web"),
  "web build must use deploy/Dockerfile.web",
)
check(
  String(renderer.build?.dockerfile ?? "").includes("Dockerfile.renderer"),
  "renderer build must use deploy/Dockerfile.renderer",
)
check(
  String(executor.build?.dockerfile ?? "").includes("Dockerfile"),
  "executor build must use the executor Dockerfile",
)
for (const dockerfile of [
  "deploy/Dockerfile.api",
  "deploy/Dockerfile.web",
  "deploy/Dockerfile.renderer",
  "executor/Dockerfile",
]) {
  check(existsSync(path.join(repoRoot, dockerfile)), `build file ${dockerfile} is missing`)
}

if (problems.length > 0) {
  console.error(`compose validation failed (${problems.length} problem(s)): ${composePath}`)
  for (const problem of problems) {
    console.error(` - ${problem}`)
  }
  process.exit(1)
}

console.log(`compose validation passed: ${composePath}`)
console.log(
  "services: api (13333) + api2 (internal, shared /data), web (6868), postgres (5433), " +
    "renderer (internal 13337), executor (internal 13336); readiness checks, P7 data volume and order OK",
)

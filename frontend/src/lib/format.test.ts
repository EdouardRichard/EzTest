import { describe, expect, it } from "vitest"

import { formatBytes, formatDate, formatDateTime, formatRelativeTime } from "./format"

const pad = (value: number) => String(value).padStart(2, "0")
const localDateTime = (date: Date) =>
  `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(
    date.getHours(),
  )}:${pad(date.getMinutes())}`

describe("formatDateTime", () => {
  it("renders an RFC3339 timestamp in local time", () => {
    const value = "2026-10-31T09:05:00Z"
    expect(formatDateTime(value)).toBe(localDateTime(new Date(value)))
  })

  it("returns a dash for missing or invalid timestamps", () => {
    expect(formatDateTime(null)).toBe("—")
    expect(formatDateTime(undefined)).toBe("—")
    expect(formatDateTime("")).toBe("—")
    expect(formatDateTime("not-a-date")).toBe("—")
  })
})

describe("formatDate", () => {
  it("renders the local calendar date", () => {
    const date = new Date("2026-10-31T09:05:00Z")
    expect(formatDate("2026-10-31T09:05:00Z")).toBe(
      `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`,
    )
  })

  it("returns a dash for invalid input", () => {
    expect(formatDate("nope")).toBe("—")
  })
})

describe("formatRelativeTime", () => {
  const now = new Date("2026-10-31T12:00:00Z")

  it("describes recent timestamps", () => {
    expect(formatRelativeTime(new Date(now.getTime() - 30_000), now)).toBe("刚刚")
  })

  it("describes minutes, hours and days", () => {
    expect(formatRelativeTime(new Date(now.getTime() - 5 * 60_000), now)).toBe("5 分钟前")
    expect(formatRelativeTime(new Date(now.getTime() - 3 * 60 * 60_000), now)).toBe("3 小时前")
    expect(formatRelativeTime(new Date(now.getTime() - 2 * 24 * 60 * 60_000), now)).toBe("2 天前")
  })

  it("falls back to an absolute date for older timestamps", () => {
    const old = new Date(now.getTime() - 40 * 24 * 60 * 60_000)
    expect(formatRelativeTime(old, now)).toBe(formatDate(old.toISOString()))
  })

  it("returns a dash for invalid input", () => {
    expect(formatRelativeTime("bad", now)).toBe("—")
  })
})

describe("formatBytes", () => {
  it("formats byte sizes with one decimal for larger units", () => {
    expect(formatBytes(0)).toBe("0 B")
    expect(formatBytes(1023)).toBe("1023 B")
    expect(formatBytes(1024)).toBe("1.0 KB")
    expect(formatBytes(1536)).toBe("1.5 KB")
    expect(formatBytes(5 * 1024 * 1024)).toBe("5.0 MB")
  })

  it("returns a dash for invalid input", () => {
    expect(formatBytes(-1)).toBe("—")
    expect(formatBytes(Number.NaN)).toBe("—")
  })
})

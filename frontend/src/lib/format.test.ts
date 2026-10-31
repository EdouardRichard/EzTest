import { describe, expect, it } from "vitest"

import {
  formatBytes,
  formatDate,
  formatDateTime,
  formatDuration,
  formatRelativeTime,
} from "./format"

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

describe("formatDuration", () => {
  it("formats step durations in seconds with one decimal", () => {
    expect(formatDuration(0)).toBe("0.0s")
    expect(formatDuration(840)).toBe("0.8s")
    expect(formatDuration(8400)).toBe("8.4s")
    expect(formatDuration(59_400)).toBe("59.4s")
  })

  it("formats minute-scale durations as `1m 02s`", () => {
    expect(formatDuration(60_000)).toBe("1m 00s")
    expect(formatDuration(62_000)).toBe("1m 02s")
    expect(formatDuration(3_599_000)).toBe("59m 59s")
  })

  it("rounds the seconds field and carries into minutes", () => {
    expect(formatDuration(59_960)).toBe("1m 00s")
  })

  it("formats hour-scale durations as `1h 02m`", () => {
    expect(formatDuration(3_600_000)).toBe("1h 00m")
    expect(formatDuration(3_723_000)).toBe("1h 02m")
    expect(formatDuration(8 * 3_600_000 + 5 * 60_000)).toBe("8h 05m")
  })

  it("returns a dash for invalid input", () => {
    expect(formatDuration(-1)).toBe("—")
    expect(formatDuration(Number.NaN)).toBe("—")
    expect(formatDuration(Number.POSITIVE_INFINITY)).toBe("—")
  })
})

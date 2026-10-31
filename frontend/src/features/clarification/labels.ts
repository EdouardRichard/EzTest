import type { BadgeTone } from "@/components/ui/Badge"
import type { SSEStatus } from "@/lib/sse-client"

import type { RequirementPriority, RequirementStatus, TeamRoomPostKind } from "./types"

export const REQUIREMENT_STATUS_LABEL: Record<RequirementStatus, string> = {
  draft: "草案",
  clarified: "已澄清",
  closed: "已闭环",
  obsolete: "已过期",
}

export const REQUIREMENT_STATUS_TONE: Record<RequirementStatus, BadgeTone> = {
  draft: "warning",
  clarified: "success",
  closed: "neutral",
  obsolete: "neutral",
}

export const REQUIREMENT_PRIORITY_LABEL: Record<RequirementPriority, string> = {
  P0: "P0",
  P1: "P1",
  P2: "P2",
  P3: "P3",
}

export const TEAM_ROOM_POST_LABEL: Record<TeamRoomPostKind, string> = {
  finding: "发现",
  decision: "决策",
  blocker: "阻塞",
  handoff: "交接",
  status: "状态",
}

export const SSE_STATUS_LABEL: Record<SSEStatus, string> = {
  connecting: "版本通道连接中…",
  live: "版本通道已连接",
  reconnecting: "版本通道重连中…",
  closed: "版本通道已断开",
}

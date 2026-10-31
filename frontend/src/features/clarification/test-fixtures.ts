import type {
  Conversation,
  ConversationMessage,
  Requirement,
  RequirementDetail,
  RequirementVersionView,
  TeamRoom,
} from "./types"

const TIMESTAMP = "2026-10-31T10:00:00Z"

export const conversationFixture: Conversation = {
  id: "4001",
  title: "需求澄清 2026-10-31 10:00",
  kind: "clarification",
  status: "active",
  message_count: 2,
  last_message_at: TIMESTAMP,
  created_at: TIMESTAMP,
  summary: "用户正在澄清退款时效。",
  updated_at: TIMESTAMP,
}

export const conversationSecondFixture: Conversation = {
  ...conversationFixture,
  id: "4002",
  title: "需求澄清 2026-10-30 16:00",
  message_count: 5,
}

export const messageFixture: ConversationMessage = {
  id: "5001",
  conversation_id: "4001",
  role: "assistant",
  parts: [{ type: "text", text: "我已载入版本上下文，请描述需求。" }],
  refs: [],
  created_at: TIMESTAMP,
}

export const conversationMessageFixture = messageFixture

export const userMessageFixture: ConversationMessage = {
  id: "5002",
  conversation_id: "4001",
  role: "user",
  parts: [
    { type: "text", text: "新增退款时效：审核通过后 T+1 到账。" },
    {
      type: "attachment",
      sha256: "a".repeat(64),
      name: "退款规则V2.pdf",
      mime: "application/pdf",
      size: 2048,
    },
  ],
  refs: [],
  created_at: TIMESTAMP,
}

export const requirementCardMessageFixture: ConversationMessage = {
  id: "5003",
  conversation_id: "4001",
  role: "assistant",
  parts: [
    { type: "text", text: "我把这段拆成 1 条需求草案。" },
    {
      type: "requirement_card",
      requirement_id: "6001",
      title: "退款到账时效",
      status: "draft",
      revision: 1,
    },
  ],
  refs: [{ kind: "requirement", id: "6001" }],
  created_at: TIMESTAMP,
}

export const rollbackProposalMessageFixture: ConversationMessage = {
  id: "5004",
  conversation_id: "4001",
  role: "assistant",
  parts: [
    {
      type: "rollback_proposal",
      requirement_id: "6002",
      reason: "新需求改变了到账时效定义，需要重新确认。",
    },
  ],
  refs: [{ kind: "requirement", id: "6002" }],
  created_at: TIMESTAMP,
}

export const requirementVersionFixture: RequirementVersionView = {
  id: "6101",
  requirement_id: "6001",
  revision: 1,
  title: "退款到账时效",
  description: "退款审核通过后 T+1 个工作日原路退回；节假日顺延。",
  acceptance_criteria: "审核通过后 T+1 个工作日到账；金额与订单实付一致。",
  priority: "P1",
  constraints: {},
  target_version_suts: ["1002"],
  target_pages: ["2301"],
  target_apis: [],
  content_hash: "b".repeat(64),
  created_at: TIMESTAMP,
}

export const requirementFixture: Requirement = {
  id: "6001",
  current_version_id: "6101",
  revision: 1,
  title: "退款到账时效",
  status: "draft",
  priority: "P1",
  targets: {
    version_suts: ["1002"],
    pages: ["2301"],
    apis: [],
  },
  updated_at: TIMESTAMP,
  confirmed_at: null,
}

export const requirementSecondFixture: Requirement = {
  ...requirementFixture,
  id: "6002",
  current_version_id: "6102",
  title: "大额订单人工审核",
  status: "clarified",
  priority: "P0",
  targets: { version_suts: ["1001"], pages: ["1301"], apis: [] },
  confirmed_at: TIMESTAMP,
}

export const requirementClosedFixture: Requirement = {
  ...requirementFixture,
  id: "6003",
  title: "购物车结算金额校验",
  status: "closed",
}

export const requirementDetailFixture: RequirementDetail = {
  ...requirementFixture,
  current_version: requirementVersionFixture,
  history: [],
  confirmations: [],
  relations: [],
  attachments: [],
}

export const confirmedRequirementDetailFixture: RequirementDetail = {
  ...requirementDetailFixture,
  status: "clarified",
  confirmed_at: TIMESTAMP,
  confirmations: [
    {
      id: "7101",
      requirement_version_id: "6101",
      scope: "requirement",
      batch_id: null,
      actor_id: "9001",
      confirmed_at: TIMESTAMP,
      affected_requirement_ids: ["6001"],
    },
  ],
}

export const teamRoomFixture: TeamRoom = {
  team: "clarification",
  digest: {
    summary: "澄清团队已读取版本上下文，提出 3 条草案。",
    pinned_post_ids: ["9101"],
    token_estimate: 128,
    version: 1,
    updated_at: TIMESTAMP,
  },
  posts: [
    {
      id: "9101",
      kind: "decision",
      content: "退款时效按 T+1 处理，节假日顺延。",
      refs: [{ kind: "requirement", id: "6001" }],
      agent_run_id: "8001",
      created_at: TIMESTAMP,
    },
  ],
}

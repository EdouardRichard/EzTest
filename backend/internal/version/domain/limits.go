package domain

// P3 limits from spec §4.1. They are code constants, not configuration.
const (
	// MaxAttachmentBytes bounds one requirement attachment (10 MiB).
	MaxAttachmentBytes = 10 << 20
	// MaxAttachmentsPerRequirement bounds attachments on one requirement.
	MaxAttachmentsPerRequirement = 10
	// MaxAttachmentsPerVersion bounds attachments across a version.
	MaxAttachmentsPerVersion = 50
	// MaxAgentImageBytes bounds one image injected into an agent run (2 MiB).
	MaxAgentImageBytes = 2 << 20
	// MaxAgentImagesPerRun bounds images injected into one agent run.
	MaxAgentImagesPerRun = 2
	// MaxInjectedTextBytesPerFile bounds text injected from one file (64 KiB).
	MaxInjectedTextBytesPerFile = 64 << 10
	// MaxInjectedTextBytesPerRun bounds injected text per agent run (200 KiB).
	MaxInjectedTextBytesPerRun = 200 << 10
	// ImagePartCharEstimate is the P1 context-budget cost of one image part.
	ImagePartCharEstimate = 4096
	// MaxQuestionChars bounds a clarification question (spec §6.6).
	MaxQuestionChars = 2000
	// MaxAnswerChars bounds a clarification answer (spec §6.6).
	MaxAnswerChars = 5000
	// MaxDecisionChars bounds a decision and its rationale (spec §6.6).
	MaxDecisionChars = 5000
	// MaxDescriptionChars bounds version and requirement descriptions.
	MaxDescriptionChars = 20000
	// MaxTitleChars bounds a requirement title (spec §6.2).
	MaxTitleChars = 300
	// MaxVersionNameChars bounds a version name (spec §7.2).
	MaxVersionNameChars = 200
	// MaxVersionLabelChars bounds a version label (spec §7.2).
	MaxVersionLabelChars = 50
	// MaxCodeBranchChars bounds the branch snapshot (spec §7.2).
	MaxCodeBranchChars = 200
	// MaxAcceptanceCriteriaChars bounds acceptance criteria (spec §6.2).
	MaxAcceptanceCriteriaChars = 10000
	// MaxTopicChars bounds a decision topic (spec §6.6).
	MaxTopicChars = 300
	// MaxAttachmentFilenameChars bounds the sanitized attachment filename.
	MaxAttachmentFilenameChars = 120
)

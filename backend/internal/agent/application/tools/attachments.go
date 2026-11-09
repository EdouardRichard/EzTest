package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// ImageSkippedMarker renders the R-A5-5 placeholder a compaction summary uses
// for an image that is not folded into the summary. The image bytes never enter
// the summary; the attachment id is the lazy re-read handle. A missing
// (zero/unknown) id is rendered as "unknown" instead of fabricating an id.
func ImageSkippedMarker(attachmentID int64) string {
	if attachmentID <= 0 {
		return "[image skipped: attachment_id=unknown]"
	}
	return fmt.Sprintf("[image skipped: attachment_id=%d]", attachmentID)
}

type attachmentList struct{ deps Deps }

func (t *attachmentList) Name() string { return ToolAttachmentList }

func (t *attachmentList) Description() string {
	return "列出当前版本/会话的附件元数据（id/name/mime/size/status）。不返回文件内容。"
}

func (t *attachmentList) Parameters() json.RawMessage { return emptyObjectSchema }

func (t *attachmentList) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params struct{}
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	if t.deps.Attachment == nil {
		return nil, missingDep("attachment")
	}
	items, err := t.deps.Attachment.ListAttachments(ctx, scope.VersionID, scope.ConversationID)
	if err != nil {
		return nil, mapPortError(err)
	}
	attachments := make([]map[string]any, 0, len(items))
	for _, item := range items {
		entry := map[string]any{
			"id":     idString(item.ID),
			"name":   item.Name,
			"mime":   item.MIME,
			"size":   item.Size,
			"status": item.Status,
		}
		if item.ConversationID != 0 {
			entry["conversation_id"] = idString(item.ConversationID)
		}
		if item.RequirementID != 0 {
			entry["requirement_id"] = idString(item.RequirementID)
		}
		attachments = append(attachments, entry)
	}
	return map[string]any{
		"version_id":  idString(scope.VersionID),
		"attachments": attachments,
		"count":       len(attachments),
	}, nil
}

type attachmentReadTextParams struct {
	AttachmentID string `json:"attachment_id"`
	Offset       int    `json:"offset"`
	MaxChars     int    `json:"max_chars"`
}

type attachmentReadText struct{ deps Deps }

func (t *attachmentReadText) Name() string { return ToolAttachmentReadText }

func (t *attachmentReadText) Description() string {
	return "分页读取已提取的附件纯文本（offset 从 0 开始，max_chars 默认 8000，最多 20000，超出按 20000 截断）。响应返回 returned_chars 与 next_offset；必须按 next_offset 前进，禁止按请求的 max_chars 跳过内容。返回 status/error/retryable；pending/processing 时明确提示“解析中，可重试”，failed 时返回具体失败原因，不抛状态冲突错误。若附件含图片，响应还返回 images[]（image_id/seq/sha256/mime/width/height/status/caption/error）；caption 由平台视觉分析生成，优先阅读 images[].caption；caption 为空或 status=failed 时说明图片分析未完成。"
}

func (t *attachmentReadText) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"attachment_id": map[string]any{
			"type":        "string",
			"description": "附件 ID（十进制字符串）。",
		},
		"offset": map[string]any{
			"type":        "integer",
			"minimum":     0,
			"description": "文本起始偏移，默认 0。",
		},
		"max_chars": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"description": "本次最多返回的字符数，默认 8000，超过 20000 按 20000 截断。",
		},
	}, "attachment_id")
}

func (t *attachmentReadText) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	params := attachmentReadTextParams{MaxChars: DefaultToolResultReadChars}
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	attachmentID, toolErr := parsePositiveID("attachment_id", params.AttachmentID)
	if toolErr != nil {
		return nil, toolErr
	}
	if params.Offset < 0 {
		return nil, Invalid("offset", "must not be negative")
	}
	if params.MaxChars <= 0 {
		return nil, Invalid("max_chars", "must be positive")
	}
	// R-A6-2 caps an oversized page instead of rejecting it: the model still
	// advances by the actual returned count below.
	if params.MaxChars > MaxToolResultReadChars {
		params.MaxChars = MaxToolResultReadChars
	}
	if t.deps.Attachment == nil {
		return nil, missingDep("attachment")
	}
	page, err := t.deps.Attachment.ReadAttachmentText(ctx, scope.VersionID, scope.ConversationID, attachmentID, params.Offset, params.MaxChars)
	if err != nil {
		return nil, mapPortError(err)
	}
	returned := utf8.RuneCountInString(page.Text)
	nextOffset := page.Offset + returned
	if page.TotalChars > 0 && nextOffset > page.TotalChars {
		nextOffset = page.TotalChars
	}
	return map[string]any{
		"attachment_id":  idString(attachmentID),
		"name":           page.Name,
		"mime":           page.MIME,
		"status":         page.Status,
		"offset":         page.Offset,
		"text":           page.Text,
		"returned_chars": returned,
		"next_offset":    nextOffset,
		"total_chars":    page.TotalChars,
		"truncated":      page.Truncated,
		"error":          page.Error,
		"retryable":      page.Retryable,
		"images":         attachmentImagesPayload(page.Images),
	}, nil
}

// attachmentImagesPayload renders the persisted image rows for the tool JSON.
// The bytes never enter the tool output: SHA-256 is the content reference.
func attachmentImagesPayload(images []AttachmentImageDetail) []map[string]any {
	result := make([]map[string]any, 0, len(images))
	for _, image := range images {
		result = append(result, map[string]any{
			"image_id": idString(image.ID),
			"seq":      image.Seq,
			"sha256":   image.SHA256,
			"mime":     image.MIME,
			"width":    image.Width,
			"height":   image.Height,
			"status":   image.Status,
			"caption":  image.Caption,
			"error":    image.Error,
		})
	}
	return result
}

type attachmentImageRefParams struct {
	AttachmentID string `json:"attachment_id"`
}

type attachmentImageRef struct{ deps Deps }

func (t *attachmentImageRef) Name() string { return ToolAttachmentImageRef }

func (t *attachmentImageRef) Description() string {
	return "返回图片附件的引用元数据（sha256/url/mime）与已生成的 images[]（含平台生成的 caption）；不会重新运行视觉分析。caption 为空或 status=failed 时说明图片分析未完成。"
}

func (t *attachmentImageRef) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"attachment_id": map[string]any{
			"type":        "string",
			"description": "图片附件 ID（十进制字符串）。",
		},
	}, "attachment_id")
}

func (t *attachmentImageRef) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params attachmentImageRefParams
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	attachmentID, toolErr := parsePositiveID("attachment_id", params.AttachmentID)
	if toolErr != nil {
		return nil, toolErr
	}
	if t.deps.Attachment == nil {
		return nil, missingDep("attachment")
	}
	image, err := t.deps.Attachment.AttachmentImageRef(ctx, scope.VersionID, scope.ConversationID, attachmentID)
	if err != nil {
		return nil, mapPortError(err)
	}
	return map[string]any{
		"attachment_id": idString(attachmentID),
		"sha256":        image.SHA256,
		"url":           image.URL,
		"mime":          image.MIME,
		"images":        attachmentImagesPayload(image.Images),
	}, nil
}

package tools

import (
	"context"
	"encoding/json"
)

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
	return "分页读取已提取的附件纯文本（offset 从 0 开始，max_chars 默认 8000，最多 20000）。"
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
			"maximum":     20000,
			"description": "本次最多返回的字符数，默认 8000。",
		},
	}, "attachment_id")
}

func (t *attachmentReadText) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	params := attachmentReadTextParams{MaxChars: 8000}
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
	if params.MaxChars <= 0 || params.MaxChars > 20000 {
		return nil, Invalid("max_chars", "must be between 1 and 20000")
	}
	if t.deps.Attachment == nil {
		return nil, missingDep("attachment")
	}
	page, err := t.deps.Attachment.ReadAttachmentText(ctx, scope.VersionID, scope.ConversationID, attachmentID, params.Offset, params.MaxChars)
	if err != nil {
		return nil, mapPortError(err)
	}
	return map[string]any{
		"attachment_id": idString(attachmentID),
		"name":          page.Name,
		"mime":          page.MIME,
		"offset":        page.Offset,
		"text":          page.Text,
		"total_chars":   page.TotalChars,
		"truncated":     page.Truncated,
	}, nil
}

type attachmentImageRefParams struct {
	AttachmentID string `json:"attachment_id"`
}

type attachmentImageRef struct{ deps Deps }

func (t *attachmentImageRef) Name() string { return ToolAttachmentImageRef }

func (t *attachmentImageRef) Description() string {
	return "返回图片附件的引用元数据（sha256/url/mime）。P1 不做视觉理解，只登记引用。"
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
	}, nil
}

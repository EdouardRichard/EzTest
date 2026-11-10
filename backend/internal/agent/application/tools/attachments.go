package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	// ImageOffset is the 0-based row offset of the image caption page. It is
	// only meaningful on the first text page (offset == 0).
	ImageOffset int `json:"image_offset"`
	// ImageLimit bounds how many image rows one page carries (1..20).
	ImageLimit int `json:"image_limit"`
}

// Tool-layer image paging limits (R2-C-01). The version layer returns every
// persisted image caption; only the tool result enters the model context, so
// budgeting/pagination happens here.
const (
	// DefaultAttachmentReadImageLimit is the default image rows per page.
	DefaultAttachmentReadImageLimit = 20
	// MaxAttachmentReadImageLimit caps a requested page size.
	MaxAttachmentReadImageLimit = 20
	// MaxAttachmentReadImageCaptionChars bounds the total caption characters of
	// one image page. A caption that would cross the budget moves whole to the
	// next page; only a page whose first caption alone exceeds it is truncated.
	MaxAttachmentReadImageCaptionChars = 4000
)

// attachmentReadImagePaging is the image-paging view nested in the attachment
// completeness summary.
type attachmentReadImagePaging struct {
	Offset     int  `json:"offset"`
	Limit      int  `json:"limit"`
	Returned   int  `json:"returned"`
	Total      int  `json:"total"`
	Truncated  bool `json:"truncated"`
	NextOffset int  `json:"next_offset"`
}

// attachmentReadSummary is the R2-C-03 completeness summary. It is the first
// key of the read_text result (Go marshals map keys in lexical order), so it
// survives the 2000-rune spill head even when the text itself is spilled.
type attachmentReadSummary struct {
	Status         string                     `json:"status"`
	ExtractionDone bool                       `json:"extraction_done"`
	TotalChars     int                        `json:"total_chars"`
	ReturnedOffset int                        `json:"returned_offset"`
	ReturnedChars  int                        `json:"returned_chars"`
	NextOffset     int                        `json:"next_offset"`
	TextTruncated  bool                       `json:"text_truncated"`
	TextComplete   bool                       `json:"text_complete"`
	ImageCount     int                        `json:"image_count"`
	ImagesIncluded bool                       `json:"images_included"`
	ImagePaging    *attachmentReadImagePaging `json:"image_paging,omitempty"`
	Note           string                     `json:"note"`
}

// attachmentReadCompletenessNote is the fixed part of the summary note: it
// tells the model which total_chars is authoritative.
const attachmentReadCompletenessNote = "完整性判断依据本对象：total_chars 是附件全文长度，text_complete=true 表示文本已读完；不要用工具结果外层信封（head/tail/content_ref）的 total_chars 或 truncated 判断附件是否缺失。"

type attachmentReadText struct{ deps Deps }

func (t *attachmentReadText) Name() string { return ToolAttachmentReadText }

func (t *attachmentReadText) Description() string {
	return "分页读取已提取的附件纯文本（offset 从 0 开始，max_chars 默认 8000，最多 20000，超出按 20000 截断）。响应返回 returned_chars 与 next_offset；必须按 next_offset 前进，禁止按请求的 max_chars 跳过内容。返回 status/error/retryable；pending/processing 时明确提示“解析中，可重试”，failed 时返回具体失败原因，不抛状态冲突错误。若附件含图片，images[] 仅随 offset=0 返回；image_offset/image_limit 按图片行分页（image_limit 默认 20、最多 20，单页 caption 合计最多 4000 字符，整条 caption 不拆分，仅本页第一张自身超预算时截断并标 caption_truncated）。images_truncated=true 时还有图片未返回，必须用同一 attachment_id、offset=0、image_offset=next_image_offset 再取一页；后续文本页只返回 image_count。caption 由平台视觉分析生成，优先阅读 images[].caption；caption 为空或 status=failed 时说明图片分析未完成。判断附件完整性只看顶层 attachment 摘要（total_chars 是全文长度，text_complete=true 表示已读完），不要用工具结果外层 head/tail 信封的 total_chars/truncated 推断；内容被 spill 时用 tool_result.read(content_ref, offset, limit) 读回完整结果。"
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
		"image_offset": map[string]any{
			"type":        "integer",
			"minimum":     0,
			"description": "图片 caption 页的起始行偏移（0-based），默认 0；仅 offset=0 时有效。",
		},
		"image_limit": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"description": "本页最多返回的图片数，默认 20，超过 20 按 20 截断。",
		},
	}, "attachment_id")
}

func (t *attachmentReadText) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	params := attachmentReadTextParams{MaxChars: DefaultToolResultReadChars, ImageLimit: DefaultAttachmentReadImageLimit}
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
	if params.ImageOffset < 0 {
		return nil, Invalid("image_offset", "must not be negative")
	}
	if params.ImageLimit <= 0 {
		return nil, Invalid("image_limit", "must be positive")
	}
	if params.ImageLimit > MaxAttachmentReadImageLimit {
		params.ImageLimit = MaxAttachmentReadImageLimit
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
	output := map[string]any{
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
		"image_count":    page.ImageCount,
	}
	summary := attachmentReadSummary{
		Status:         page.Status,
		ExtractionDone: page.Status == "done",
		TotalChars:     page.TotalChars,
		ReturnedOffset: page.Offset,
		ReturnedChars:  returned,
		NextOffset:     nextOffset,
		TextTruncated:  page.Truncated,
		TextComplete:   page.Status == "done" && (page.TotalChars == 0 || nextOffset >= page.TotalChars),
		ImageCount:     page.ImageCount,
		ImagesIncluded: page.Offset == 0,
		Note:           attachmentReadCompletenessNote,
	}
	// Images are a first-page-only payload: repeating them on every text page
	// would grow the Host context without adding information. The page cursor
	// (image_offset/image_limit) makes every persisted caption reachable.
	if page.Offset == 0 {
		imageOffset := params.ImageOffset
		if imageOffset > len(page.Images) {
			imageOffset = len(page.Images)
		}
		images, nextImageOffset, imagesTruncated := paginateAttachmentImages(page.Images, imageOffset, params.ImageLimit)
		output["images"] = images
		output["image_offset"] = imageOffset
		output["image_returned"] = len(images)
		output["images_truncated"] = imagesTruncated
		output["next_image_offset"] = nextImageOffset
		summary.ImagePaging = &attachmentReadImagePaging{
			Offset:     imageOffset,
			Limit:      params.ImageLimit,
			Returned:   len(images),
			Total:      page.ImageCount,
			Truncated:  imagesTruncated,
			NextOffset: nextImageOffset,
		}
		if imagesTruncated {
			remaining := page.ImageCount - nextImageOffset
			if remaining > 0 {
				summary.Note += fmt.Sprintf("还有 %d 张图片的 caption 未返回，请用 offset=0、image_offset=%d 继续读取。", remaining, nextImageOffset)
			}
		}
	}
	if page.Status == "done" && nextOffset < page.TotalChars {
		summary.Note += fmt.Sprintf("还有 %d 字符未读，请继续调用本工具并使用 offset=%d。", page.TotalChars-nextOffset, nextOffset)
	}
	output["attachment"] = summary
	return output, nil
}

// paginateAttachmentImages returns one page of image rows. Captions are never
// split: the loop stops before an image whose whole caption would exceed the
// page budget, unless it is the first image of the page (a single oversized
// caption can still make progress). nextOffset is the first row not emitted.
func paginateAttachmentImages(images []AttachmentImageDetail, imageOffset, imageLimit int) (
	page []map[string]any, nextOffset int, truncated bool,
) {
	if imageOffset < 0 {
		imageOffset = 0
	}
	if imageOffset > len(images) {
		imageOffset = len(images)
	}
	if imageLimit < 1 {
		imageLimit = 1
	}
	if imageLimit > MaxAttachmentReadImageLimit {
		imageLimit = MaxAttachmentReadImageLimit
	}
	remaining := MaxAttachmentReadImageCaptionChars
	page = make([]map[string]any, 0, imageLimit)
	index := imageOffset
	for index < len(images) && len(page) < imageLimit {
		image := images[index]
		entry := attachmentImagePayload(image)
		captionRunes := utf8.RuneCountInString(image.Caption)
		if captionRunes > remaining {
			if len(page) > 0 {
				// The whole caption belongs to the next page: never cut it.
				break
			}
			// Degenerate case: the first caption of the page alone exceeds the
			// page budget. Truncate just this one and advance the cursor so the
			// model cannot loop on the same row forever.
			entry["caption"] = string([]rune(image.Caption)[:remaining])
			entry["caption_truncated"] = true
			page = append(page, entry)
			index++
			break
		}
		remaining -= captionRunes
		page = append(page, entry)
		index++
	}
	nextOffset = index
	truncated = nextOffset < len(images)
	return page, nextOffset, truncated
}

// attachmentImagesPayload renders the persisted image rows for the tool JSON.
// The bytes never enter the tool output: SHA-256 is the content reference.
func attachmentImagesPayload(images []AttachmentImageDetail) []map[string]any {
	result := make([]map[string]any, 0, len(images))
	for _, image := range images {
		result = append(result, attachmentImagePayload(image))
	}
	return result
}

// attachmentImagePayload renders one persisted image row.
func attachmentImagePayload(image AttachmentImageDetail) map[string]any {
	return map[string]any{
		"image_id": idString(image.ID),
		"seq":      image.Seq,
		"sha256":   image.SHA256,
		"mime":     image.MIME,
		"width":    image.Width,
		"height":   image.Height,
		"status":   image.Status,
		"caption":  image.Caption,
		"error":    image.Error,
	}
}

type attachmentImageRefParams struct {
	AttachmentID string `json:"attachment_id"`
	// ImageID is the attachment_image.id exposed by attachment.read_text as
	// images[].image_id. attachment_id and image_id are mutually exclusive
	// (R2-C-02): both are decimal snowflake ids but belong to different tables.
	ImageID string `json:"image_id"`
}

type attachmentImageRef struct{ deps Deps }

func (t *attachmentImageRef) Name() string { return ToolAttachmentImageRef }

func (t *attachmentImageRef) Description() string {
	return "返回图片附件或含内嵌图片的文档（如 docx）的引用元数据（sha256/url/mime）与已生成的 images[]（含平台生成的 caption）；不会重新运行视觉分析。attachment_id 与 image_id 二选一（必须且只能传一个）：attachment_id 是附件 id，即 attachment.read_text 顶层返回的 attachment_id；image_id 是 attachment.read_text 的 images[].image_id（图片行 id，即 attachment_image.id）。两者都是十进制字符串，禁止混用，禁止把 images[].image_id 当作 attachment_id。独立图片附件没有持久化 caption 时返回空 images[] 并在 note 中说明；caption 为空或 status=failed 时说明图片分析未完成。"
}

func (t *attachmentImageRef) Parameters() json.RawMessage {
	// schemaObject cannot express an either/or contract: hand-build the oneOf so
	// the model sees that exactly one of the two id kinds is accepted. oneOf (not
	// anyOf) is required here because anyOf also matches when both ids are present.
	schema := map[string]any{
		"type":                 "object",
		"description":          "attachment_id 与 image_id 二选一（必须且只能传一个）。",
		"additionalProperties": false,
		"properties": map[string]any{
			"attachment_id": map[string]any{
				"type":        "string",
				"description": "附件 ID（十进制字符串），即 attachment.read_text 顶层返回的 attachment_id。与 image_id 二选一。",
			},
			"image_id": map[string]any{
				"type":        "string",
				"description": "attachment.read_text 返回的 images[].image_id（图片行 ID，十进制字符串）。与 attachment_id 二选一，不能把它当 attachment_id 使用。",
			},
		},
		"oneOf": []any{
			map[string]any{
				"description": "按附件 id 取引用（read_text 顶层 attachment_id）",
				"required":    []string{"attachment_id"},
			},
			map[string]any{
				"description": "按图片行 id 取父附件引用（read_text images[].image_id）",
				"required":    []string{"image_id"},
			},
		},
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return emptyObjectSchema
	}
	return raw
}

func (t *attachmentImageRef) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	var params attachmentImageRefParams
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	attachmentRaw := strings.TrimSpace(params.AttachmentID)
	imageRaw := strings.TrimSpace(params.ImageID)
	if t.deps.Attachment == nil {
		return nil, missingDep("attachment")
	}
	var image AttachmentImage
	switch {
	case attachmentRaw != "" && imageRaw != "":
		return nil, attachmentImageRefIDChoiceError(
			"provide either attachment_id or image_id, not both",
			"attachment_id 与 image_id 只能传一个（二选一）：请只保留其中一个参数后重试。",
		)
	case attachmentRaw != "":
		attachmentID, toolErr := parsePositiveID("attachment_id", attachmentRaw)
		if toolErr != nil {
			return nil, toolErr
		}
		ref, err := t.deps.Attachment.AttachmentImageRef(ctx, scope.VersionID, scope.ConversationID, attachmentID)
		if err != nil {
			return nil, attachmentImageRefError(err, "attachment_id", attachmentRaw)
		}
		image = ref
	case imageRaw != "":
		imageID, toolErr := parsePositiveID("image_id", imageRaw)
		if toolErr != nil {
			return nil, toolErr
		}
		ref, err := t.deps.Attachment.AttachmentImageRefByImage(ctx, scope.VersionID, scope.ConversationID, imageID)
		if err != nil {
			return nil, attachmentImageRefError(err, "image_id", imageRaw)
		}
		image = ref
	default:
		return nil, attachmentImageRefIDChoiceError(
			"exactly one of attachment_id or image_id is required",
			"请传 attachment_id 或 image_id（二选一）：attachment_id 来自 read_text 顶层，image_id 来自 images[].image_id。",
		)
	}
	return map[string]any{
		"attachment_id": idString(image.AttachmentID),
		"sha256":        image.SHA256,
		"url":           image.URL,
		"mime":          image.MIME,
		"images":        attachmentImagesPayload(image.Images),
		"note":          image.Note,
	}, nil
}

// attachmentImageRefIDChoiceError builds the shared invalid_argument for the
// exactly-one-id contract. The message stays English (backend envelope); the
// actionable, localized correction lives in Hint and Details.hint, which the
// frontend ToolCallBlock renders.
func attachmentImageRefIDChoiceError(message, hint string) *Error {
	err := InvalidField("attachment_id", message)
	err.Hint = hint
	err.Details = map[string]any{"field": "attachment_id", "hint": hint}
	return err
}

// attachmentImageRefError turns a not_found port failure into an actionable
// error. The message stays English (the frontend localizes generic errors), while
// field/hint carry the correction the Host needs: attachment_id (read_text top
// level) and image_id (read_text images[].image_id) are different tables even
// though both are decimal ids. An unknown id cannot start succeeding on retry,
// so Retryable stays false (R2-C-02).
func attachmentImageRefError(err error, field, rawID string) error {
	mapped := mapPortError(err)
	var toolErr *Error
	if !errors.As(mapped, &toolErr) || toolErr.Code != CodeNotFound {
		return mapped
	}
	message := "attachment not found in the current conversation"
	hint := fmt.Sprintf("未找到 attachment_id=%s：attachment_id 是 attachment.read_text 顶层返回的附件 id，不是 images[].image_id（图片行 id）；请确认传入的是顶层 attachment_id，或改用 image_id 参数传 images[].image_id。", rawID)
	if field == "image_id" {
		message = "attachment image not found in the current conversation"
		hint = fmt.Sprintf("未找到 image_id=%s：image_id 是 attachment.read_text 返回的 images[].image_id（图片行 id），不是顶层 attachment_id；请确认该图片属于当前会话，或改用 attachment_id 参数传顶层 attachment_id。", rawID)
	}
	return &Error{
		Code:      CodeNotFound,
		Message:   message,
		Retryable: false,
		Field:     field,
		Hint:      hint,
		Details:   map[string]any{"field": field, "hint": hint},
	}
}

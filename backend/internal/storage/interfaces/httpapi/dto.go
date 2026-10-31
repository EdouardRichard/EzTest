package storageapi

import "github.com/EdouardRichard/EzTest/backend/internal/storage/domain"

// objectDTO is the public content metadata. The filestore relative path and
// absolute data root are never included.
type objectDTO struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	MIME   string `json:"mime"`
	URL    string `json:"url"`
}

func toObjectDTO(object storage.Object) objectDTO {
	return objectDTO{
		SHA256: object.SHA256,
		Size:   object.Size,
		MIME:   object.MIME,
		URL:    "/api/content/" + object.SHA256,
	}
}

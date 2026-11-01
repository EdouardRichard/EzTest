package httpapi

import searchapp "github.com/EdouardRichard/EzTest/backend/internal/search/application"

// searchResultDTO is the `SearchResultView` response body (openapi.yaml). Every
// slice is rendered as `[]` rather than `null`, including an empty degraded
// group and a total provider outage.
type searchResultDTO struct {
	Groups    []searchGroupDTO `json:"groups"`
	Total     int              `json:"total"`
	Truncated bool             `json:"truncated"`
	Degraded  bool             `json:"degraded"`
}

// searchGroupDTO is one `SearchGroup`. Type is one of the five frozen enum
// values and Items preserves the provider order.
type searchGroupDTO struct {
	Type  string          `json:"type"`
	Items []searchItemDTO `json:"items"`
}

// searchItemDTO is one `SearchItem`. IDs cross the JSON boundary as decimal
// strings and url is the server-built 0.2 workspace deep link.
type searchItemDTO struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Subtitle     string `json:"subtitle"`
	VersionID    string `json:"version_id"`
	VersionTitle string `json:"version_title"`
	Status       string `json:"status"`
	URL          string `json:"url"`
}

func toSearchResultDTO(snapshot searchapp.Snapshot) searchResultDTO {
	groups := make([]searchGroupDTO, 0, len(snapshot.Groups))
	for _, group := range snapshot.Groups {
		items := make([]searchItemDTO, 0, len(group.Items))
		for _, item := range group.Items {
			items = append(items, searchItemDTO{
				ID:           item.ID,
				Title:        item.Title,
				Subtitle:     item.Subtitle,
				VersionID:    item.VersionID,
				VersionTitle: item.VersionTitle,
				Status:       item.Status,
				URL:          item.URL,
			})
		}
		groups = append(groups, searchGroupDTO{Type: group.Type, Items: items})
	}
	return searchResultDTO{
		Groups:    groups,
		Total:     snapshot.Total,
		Truncated: snapshot.Truncated,
		Degraded:  snapshot.Degraded,
	}
}

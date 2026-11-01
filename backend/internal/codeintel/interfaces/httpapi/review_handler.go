package codeintelapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// ReviewServicePort is the application surface the review handler needs.
type ReviewServicePort interface {
	List(ctx context.Context, versionID, factID int64) ([]codeintel.FactReview, error)
	Create(ctx context.Context, versionID, factID, reviewerID int64, verdict codeintel.ReviewVerdict, reason string) (codeintel.FactReview, error)
}

// ReviewHandler serves the two fact-review operations (spec §4.2 #22–#23).
type ReviewHandler struct {
	reviews ReviewServicePort
}

// NewReviewHandler builds the fact-review handler.
func NewReviewHandler(reviews ReviewServicePort) *ReviewHandler {
	return &ReviewHandler{reviews: reviews}
}

// Register mounts the fact-review routes behind the authenticated middleware.
func (h *ReviewHandler) Register(mux *http.ServeMux, authenticated func(http.Handler) http.Handler) {
	mux.Handle("GET /api/versions/{versionId}/code/facts/{factId}/reviews", authenticated(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/versions/{versionId}/code/facts/{factId}/reviews", authenticated(http.HandlerFunc(h.Create)))
}

// codeFactReviewDTO is CodeFactReviewView.
type codeFactReviewDTO struct {
	ID          string    `json:"id"`
	FactID      string    `json:"fact_id"`
	FactVersion int       `json:"fact_version"`
	RepoID      string    `json:"repo_id"`
	CommitSHA   string    `json:"commit_sha"`
	Verdict     string    `json:"verdict"`
	Reason      string    `json:"reason"`
	ReviewerID  string    `json:"reviewer_id"`
	CreatedAt   time.Time `json:"created_at"`
}

func toReviewDTO(review codeintel.FactReview) codeFactReviewDTO {
	return codeFactReviewDTO{
		ID:          platformid.Format(review.ID),
		FactID:      platformid.Format(review.FactID),
		FactVersion: review.FactVersion,
		RepoID:      platformid.Format(review.RepoID),
		CommitSHA:   review.CommitSHA,
		Verdict:     review.Verdict,
		Reason:      review.Reason,
		ReviewerID:  platformid.Format(review.ReviewerID),
		CreatedAt:   review.CreatedAt,
	}
}

// codeFactReviewCreateRequest is the POST payload.
type codeFactReviewCreateRequest struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// List GET /api/versions/{versionId}/code/facts/{factId}/reviews.
func (h *ReviewHandler) List(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	factID, err := pathID(r, "factId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	reviews, err := h.reviews.List(r.Context(), versionID, factID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]codeFactReviewDTO, 0, len(reviews))
	for _, review := range reviews {
		items = append(items, toReviewDTO(review))
	}
	httpx.List(w, http.StatusOK, items, "", false)
}

// Create POST /api/versions/{versionId}/code/facts/{factId}/reviews.
func (h *ReviewHandler) Create(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	factID, err := pathID(r, "factId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request codeFactReviewCreateRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	verdict := codeintel.ReviewVerdict(strings.TrimSpace(request.Verdict))
	if !verdict.Valid() {
		writeError(w, r, unprocessable("invalid_verdict", "verdict must be correct or incorrect"))
		return
	}
	review, err := h.reviews.Create(r.Context(), versionID, factID, actorID, verdict, request.Reason)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toReviewDTO(review))
}

package knowledgeapp

import (
	"context"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Structured retrieval (spec §10.2): knowledge.search/knowledge.read.
//
// The 0.2.1 knowledge migration adds the structured bindings but no pg_trgm
// extension or trigram index (the base migration only adds columns), and this
// task is not allowed to add migrations. The repository therefore keeps the
// existing relevance support as the candidate stage: ListForInjection already
// returns only approved rows visible to the version's SUT families, applies the
// strict JSONB binding containment and orders by the stored tsvector ts_rank.
// Retrieval then applies the pg_trgm-compatible character-trigram similarity in
// Go, which gives Chinese queries the fuzzy recall the 'simple' full-text
// configuration cannot provide, and ranks the bounded candidate window
// deterministically. The binding filter primitive itself is never weakened: the
// strict pass uses the repository's containment query unchanged.
const (
	// DefaultRetrievalLimit is the knowledge.search page size when the caller
	// omits limit (the injection default of five is too small for a tool page).
	DefaultRetrievalLimit = 10
	// MaxRetrievalLimit caps one knowledge.search page so one tool result can
	// never dominate the model context.
	MaxRetrievalLimit = 50
	// MaxRetrievalTextRunes bounds the free-text query (spec §12.3 search DTO
	// uses a similarly small query budget).
	MaxRetrievalTextRunes = 512

	// retrievalCandidateLimit is the number of approved rows the repository
	// stage may return per pass. Strict and generic passes are merged before
	// pagination; the cap keeps one search bounded even when the store grows.
	retrievalCandidateLimit = 200
	// retrievalCandidateCap bounds the widened window used by deep pages.
	retrievalCandidateCap = 500
	// retrievalScoreContentRunes bounds the content window used for similarity
	// scoring so a 20000-rune item cannot make scoring unbounded.
	retrievalScoreContentRunes = 1000
	// maxRetrievalPage rejects absurd page numbers before int multiplication.
	maxRetrievalPage = 10000
)

// Query is one knowledge.search request. Bindings are optional structured SUT
// filters; unset fields do not constrain the result. SystemKeys are the SUT
// families visible to the calling version (resolved by the caller because the
// knowledge context never reads another context's tables); only global items
// and system items whose system_key is in the set are visible. VersionID is the
// caller's version scope: it is validated here and carried for the caller, not
// used for a cross-context lookup.
type Query struct {
	VersionID     int64
	SystemKeys    []string
	SystemID      *int64
	EnvironmentID *int64
	PageKey       *string
	APIKey        *string
	AccountRole   *string
	Text          string
	Page          int
	Limit         int
}

// RetrievalHit is one ranked knowledge row returned by knowledge.search. It is
// deliberately bounded: IDs are decimal strings at the tool boundary and the
// body is only the existing 500-rune Snippet; the full item is fetched through
// knowledge.read.
type RetrievalHit struct {
	ID      int64
	Title   string
	Kind    string
	Score   float64
	Snippet string
}

// RetrievalPage is one ranked page of knowledge.search hits.
type RetrievalPage struct {
	Items   []RetrievalHit
	Page    int
	Limit   int
	HasMore bool
}

// Retrieve runs the structured retrieval policy: approved-only, version
// visibility, strict binding containment first, a compatible generic fallback
// second, character-trigram relevance and deterministic top-K pagination.
//
// Fallback policy: the repository's binding containment is strict (a dimension
// set on the query must be present and equal on the item), so a page/API/role
// specific query would otherwise never see knowledge that is intentionally
// generic (bindings unset) or only partially bound. Retrieve therefore runs a
// second, unfiltered pass and only admits items that do not *conflict* with any
// queried dimension. Strict hits always outrank fallback hits. The containment
// primitive itself is untouched.
func (s *Service) Retrieve(ctx context.Context, query Query) (RetrievalPage, error) {
	if err := s.requireRepo(); err != nil {
		return RetrievalPage{}, err
	}
	if query.VersionID <= 0 {
		return RetrievalPage{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if query.Page < 0 {
		return RetrievalPage{}, platform.Invalid("page", "must not be negative")
	}
	page := query.Page
	if page == 0 {
		page = 1
	}
	if page > maxRetrievalPage {
		return RetrievalPage{}, platform.Invalid("page", "must be at most 10000")
	}
	if query.Limit < 0 {
		return RetrievalPage{}, platform.Invalid("limit", "must not be negative")
	}
	limit := query.Limit
	if limit == 0 {
		limit = DefaultRetrievalLimit
	}
	if limit > MaxRetrievalLimit {
		limit = MaxRetrievalLimit
	}
	text := strings.TrimSpace(query.Text)
	if utf8.RuneCountInString(text) > MaxRetrievalTextRunes {
		return RetrievalPage{}, platform.Invalid("text", "must be at most 512 characters")
	}
	bindings, err := knowledge.Bindings{
		SystemID:      query.SystemID,
		EnvironmentID: query.EnvironmentID,
		PageKey:       query.PageKey,
		APIKey:        query.APIKey,
		AccountRole:   query.AccountRole,
	}.Normalize()
	if err != nil {
		return RetrievalPage{}, err
	}
	systemKeys := normalizeSystemKeys(query.SystemKeys)
	candidateLimit := page * limit
	if candidateLimit < retrievalCandidateLimit {
		candidateLimit = retrievalCandidateLimit
	}
	if candidateLimit > retrievalCandidateCap {
		candidateLimit = retrievalCandidateCap
	}

	strictCandidates := make([]knowledge.Item, 0)
	if !bindings.IsZero() {
		strictCandidates, err = s.repo.ListForInjection(ctx, knowledge.InjectionQuery{
			SystemKeys: systemKeys,
			Query:      text,
			Bindings:   bindings,
			Limit:      candidateLimit,
		})
		if err != nil {
			return RetrievalPage{}, err
		}
	}
	genericCandidates, err := s.repo.ListForInjection(ctx, knowledge.InjectionQuery{
		SystemKeys: systemKeys,
		Query:      text,
		Limit:      candidateLimit,
	})
	if err != nil {
		return RetrievalPage{}, err
	}

	textQuery := text
	if len(retrievalTrigrams(textQuery)) == 0 {
		// Punctuation/whitespace cannot drive a trigram filter; treat it as a
		// structured-only query instead of returning nothing.
		textQuery = ""
	}
	scored := make([]scoredRetrieval, 0, len(strictCandidates)+len(genericCandidates))
	seen := make(map[int64]struct{}, len(strictCandidates)+len(genericCandidates))
	add := func(items []knowledge.Item) {
		for _, item := range items {
			if _, duplicate := seen[item.ID]; duplicate {
				continue
			}
			if !searchableItem(item, systemKeys) {
				continue
			}
			matched, total, conflict := bindingMatch(item.Bindings, bindings)
			if conflict {
				continue
			}
			strict := total > 0 && matched == total
			textScore := retrievalTextScore(textQuery, item)
			if textQuery != "" && textScore <= 0 {
				continue
			}
			score := 0.5 * textScore
			if strict {
				score += 1.0 + 0.1*float64(matched)
			} else if total > 0 {
				// A partially bound item is still more relevant than a wholly
				// generic one, but never outranks a strict containment hit.
				score += 0.1 * float64(matched)
			}
			seen[item.ID] = struct{}{}
			kind, _ := KindOf(item)
			scored = append(scored, scoredRetrieval{
				strict: strict,
				hit: RetrievalHit{
					ID:      item.ID,
					Title:   item.Title,
					Kind:    string(kind),
					Score:   roundScore(score),
					Snippet: item.Snippet(),
				},
			})
		}
	}
	add(strictCandidates)
	add(genericCandidates)

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].strict != scored[j].strict {
			return scored[i].strict
		}
		if scored[i].hit.Score != scored[j].hit.Score {
			return scored[i].hit.Score > scored[j].hit.Score
		}
		return scored[i].hit.ID > scored[j].hit.ID
	})

	offset := (page - 1) * limit
	if offset > len(scored) {
		offset = len(scored)
	}
	end := offset + limit
	if end > len(scored) {
		end = len(scored)
	}
	items := make([]RetrievalHit, 0, end-offset)
	for _, entry := range scored[offset:end] {
		items = append(items, entry.hit)
	}
	return RetrievalPage{
		Items:   items,
		Page:    page,
		Limit:   limit,
		HasMore: end < len(scored),
	}, nil
}

// Read returns one knowledge item for knowledge.read. It is deliberately read
// by opaque ID because knowledge is cross-version by design (a version reads a
// global item or a system item of its own SUT family); the adapter bridge owns
// version visibility and only exposes IDs obtained from a scoped
// search/injection path. Draft, rejected and revoked items are not readable
// (not_found): only the verified lifecycle (approved/suspect/stale) is exposed
// to agent runs, and Task 33 requires stale items to remain readable for
// diagnosis even though they no longer inject. An item that carries a
// credential/secret is hidden as well, so knowledge.read can never leak a
// secret that slipped past an earlier write path.
func (s *Service) Read(ctx context.Context, id int64) (knowledge.Item, error) {
	if err := s.requireID(id); err != nil {
		return knowledge.Item{}, err
	}
	if err := s.requireRepo(); err != nil {
		return knowledge.Item{}, err
	}
	item, err := s.repo.Get(ctx, id)
	if err != nil {
		return knowledge.Item{}, mapRepoError(err)
	}
	if !readableStatus(item.Status) || itemHasSecrets(item) {
		return knowledge.Item{}, platform.NotFound("knowledge_item")
	}
	s.decorateItemStale(ctx, &item)
	return item, nil
}

/* -------------------------------- internals ------------------------------- */

type scoredRetrieval struct {
	strict bool
	hit    RetrievalHit
}

// searchableItem is the approved-only, version-visible, secret-free read model
// of one candidate. The repository already enforces approved + visibility; the
// application re-checks both so an adapter regression cannot leak an
// unpublished or foreign-system item to a model.
func searchableItem(item knowledge.Item, systemKeys []string) bool {
	if item.Status != knowledge.StatusApproved {
		return false
	}
	if !scopeMatches(item, systemKeys) {
		return false
	}
	return !itemHasSecrets(item)
}

func readableStatus(status knowledge.Status) bool {
	switch status {
	case knowledge.StatusApproved, knowledge.StatusSuspect, knowledge.StatusStale:
		return true
	default:
		return false
	}
}

// bindingMatch compares an item's stored bindings with the queried bindings.
// matched counts the queried dimensions the item sets to the same value; total
// is the number of queried dimensions; conflict reports an item that disagrees
// on a queried dimension (which the generic fallback must exclude). An item
// that leaves a queried dimension unset is compatible (generic) but not matched.
func bindingMatch(item, query knowledge.Bindings) (matched, total int, conflict bool) {
	// right is the queried dimension: nil means "not filtered", so it does not
	// count towards total. A set query dimension that the item leaves unset is
	// compatible (generic) but not matched.
	matchID := func(left, right *int64) (bool, bool) {
		if right == nil {
			return false, false
		}
		total++
		if left == nil {
			return false, false
		}
		if *left != *right {
			return false, true
		}
		return true, false
	}
	matchText := func(left, right *string) (bool, bool) {
		if right == nil {
			return false, false
		}
		total++
		if left == nil {
			return false, false
		}
		if *left != *right {
			return false, true
		}
		return true, false
	}
	check := func(equal, differs bool) {
		if differs {
			conflict = true
			return
		}
		if equal {
			matched++
		}
	}
	check(matchID(item.SystemID, query.SystemID))
	check(matchID(item.EnvironmentID, query.EnvironmentID))
	check(matchText(item.PageKey, query.PageKey))
	check(matchText(item.APIKey, query.APIKey))
	check(matchText(item.AccountRole, query.AccountRole))
	return matched, total, conflict
}

// retrievalTextScore is the pg_trgm-style relevance of the query against the
// item's title, tags and a bounded content window. The maximum field score is
// used (the equivalent of pg_trgm word_similarity over the best matching part)
// so a long body does not dilute a title match.
func retrievalTextScore(query string, item knowledge.Item) float64 {
	if query == "" {
		return 0
	}
	score := retrievalSimilarity(query, item.Title)
	if len(item.Tags) > 0 {
		score = math.Max(score, retrievalSimilarity(query, strings.Join(item.Tags, " ")))
	}
	if item.Content != "" {
		score = math.Max(score, retrievalSimilarity(query, truncateRunes(item.Content, retrievalScoreContentRunes)))
	}
	return score
}

// retrievalSimilarity is the Jaccard similarity of the character n-gram sets
// (the pg_trgm similarity definition: common n-grams / n-gram union). Words are
// padded at both ends like pg_trgm so short CJK words such as 登录 still produce
// per-word n-grams and near-misses such as 登陆 keep a positive score. Two- and
// three-rune n-grams are both scored and the best one wins: a two-rune Chinese
// query has no internal trigram, so without character bigrams a match in the
// middle of a longer CJK word (打开登录页) would never recall.
func retrievalSimilarity(query, target string) float64 {
	return math.Max(
		retrievalNgramSimilarity(query, target, 3),
		retrievalNgramSimilarity(query, target, 2),
	)
}

func retrievalNgramSimilarity(query, target string, size int) float64 {
	queryGrams := retrievalNgrams(query, size)
	if len(queryGrams) == 0 {
		return 0
	}
	targetGrams := retrievalNgrams(target, size)
	if len(targetGrams) == 0 {
		return 0
	}
	common := 0
	for gram := range queryGrams {
		if _, ok := targetGrams[gram]; ok {
			common++
		}
	}
	union := len(queryGrams) + len(targetGrams) - common
	if union == 0 {
		return 0
	}
	return float64(common) / float64(union)
}

// retrievalTrigrams builds the distinct character-trigram set of the normalized
// text. Letter/digit boundaries separate words; each word is padded with two
// leading and one trailing space, mirroring pg_trgm's show_trgm behavior.
func retrievalTrigrams(text string) map[string]struct{} {
	return retrievalNgrams(text, 3)
}

// retrievalNgrams builds the distinct character n-gram set of the normalized
// text. Letter/digit boundaries separate words; each word is padded with two
// leading and one trailing space, mirroring pg_trgm's show_trgm behavior.
func retrievalNgrams(text string, size int) map[string]struct{} {
	if size <= 0 {
		return map[string]struct{}{}
	}
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	grams := make(map[string]struct{})
	for _, word := range words {
		runes := []rune(word)
		padded := make([]rune, 0, len(runes)+3)
		padded = append(padded, ' ', ' ')
		padded = append(padded, runes...)
		padded = append(padded, ' ')
		for index := 0; index+size <= len(padded); index++ {
			gram := string(padded[index : index+size])
			// Purely padded n-grams ("  " for a two-rune word) are shared by
			// every string and would make unrelated items match; they carry no
			// lexical signal.
			if strings.TrimSpace(gram) == "" {
				continue
			}
			grams[gram] = struct{}{}
		}
	}
	return grams
}

func roundScore(score float64) float64 {
	return math.Round(score*10000) / 10000
}

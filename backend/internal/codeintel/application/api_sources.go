package codeintelapp

// This file owns the code-first API extraction used by the explorer (spec §11
// capability #3). It reads repository text only: Gin route registrations,
// OpenAPI/Swagger documents, frontend api-client call sites and generated API
// type definitions. Browser network capture (fetch/XHR interception) is a 0.3
// dynamic source and is deliberately absent here.
//
// The extractor is pure and deterministic: files are processed in path order,
// endpoints are normalized to `METHOD /path/{id}` and deduplicated per source.
// It never returns source bodies, header/cookie values or query values; only
// method, normalized path template, parameter names, auth hint and provenance
// leave this package.

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// Static source tags. They are carried into the explorer's three-state merge so
// a reviewer can see why an endpoint is declared and/or considered used.
const (
	StaticSourceGinRoute       = "gin_route"
	StaticSourceOpenAPI        = "openapi"
	StaticSourceAPIClient      = "api_client"
	StaticSourceTypeDefinition = "type_definition"
)

// Static extraction bounds. They mirror the codeintel hard limits philosophy:
// settings may only lower them, never raise them.
const (
	DefaultStaticMaxFiles     = 400
	DefaultStaticMaxFileBytes = 1 << 20
	DefaultStaticMaxAPIs      = 500
	maxStaticPathChars        = 500
)

// SourceFile is one repository text file handed to the extractor. Content is
// never persisted; callers keep it in memory for the extraction only.
type SourceFile struct {
	Path    string
	Content string
}

// StaticAPIOptions bounds one static extraction run. A non-positive value falls
// back to the corresponding default.
type StaticAPIOptions struct {
	MaxFiles     int
	MaxFileBytes int
	MaxAPIs      int
}

// StaticAPI is one statically extracted endpoint. Declared sources feed the
// declared side of the coverage merge; api-client sources feed the used side.
type StaticAPI struct {
	Method       string
	PathTemplate string
	Params       []string
	Source       string
	File         string
	Line         int
	Name         string
	Auth         string
}

// StaticAPIExtraction is the bounded result of scanning a repository snapshot.
// Declared and Used are sorted by method, path and source.
type StaticAPIExtraction struct {
	Declared     []StaticAPI
	Used         []StaticAPI
	FilesScanned int
	FilesSkipped int
	Truncated    bool
}

// ExtractStaticAPIs scans files for the static API sources. It is the single
// entry point used by the explorer; the per-source parsers stay unexported so
// there is exactly one normalization/deduplication path.
func ExtractStaticAPIs(files []SourceFile, opts StaticAPIOptions) StaticAPIExtraction {
	maxFiles := positiveOrDefault(opts.MaxFiles, DefaultStaticMaxFiles)
	maxBytes := positiveOrDefault(opts.MaxFileBytes, DefaultStaticMaxFileBytes)
	maxAPIs := positiveOrDefault(opts.MaxAPIs, DefaultStaticMaxAPIs)

	ordered := append([]SourceFile(nil), files...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Path != ordered[j].Path {
			return ordered[i].Path < ordered[j].Path
		}
		return ordered[i].Content < ordered[j].Content
	})

	result := StaticAPIExtraction{}
	declared := map[string]StaticAPI{}
	used := map[string]StaticAPI{}
	processed := 0
	for _, file := range ordered {
		path := strings.TrimSpace(file.Path)
		lowerPath := strings.ToLower(path)
		// Test fixtures are not production declarations and must not consume
		// the per-run file budget.
		if strings.HasSuffix(lowerPath, "_test.go") {
			continue
		}
		// Irrelevant files must not consume the per-run file budget.
		if !isGoSourceFile(lowerPath) && !isFrontendSourceFile(lowerPath) && !isOpenAPISource(path, file.Content) {
			continue
		}
		if processed >= maxFiles {
			result.Truncated = true
			break
		}
		processed++
		result.FilesScanned++
		if len(file.Content) > maxBytes {
			result.FilesSkipped++
			continue
		}
		switch {
		case isGoSourceFile(lowerPath):
			for _, api := range extractGinRoutes([]SourceFile{file}) {
				mergeStaticAPI(declared, api)
			}
		case isOpenAPISource(path, file.Content):
			for _, api := range extractOpenAPIDocuments([]SourceFile{file}) {
				mergeStaticAPI(declared, api)
			}
		}
		if isFrontendSourceFile(lowerPath) {
			for _, api := range extractFrontendAPIClients([]SourceFile{file}) {
				mergeStaticAPI(used, api)
			}
			if isGeneratedTypeFile(file.Content) {
				for _, api := range extractAPITypeDefinitions([]SourceFile{file}) {
					mergeStaticAPI(declared, api)
				}
			}
		}
	}

	result.Declared = sortedStaticAPIs(declared)
	result.Used = sortedStaticAPIs(used)
	if len(result.Declared) > maxAPIs {
		result.Declared = result.Declared[:maxAPIs]
		result.Truncated = true
	}
	if len(result.Used) > maxAPIs {
		result.Used = result.Used[:maxAPIs]
		result.Truncated = true
	}
	return result
}

/* ------------------------------- Gin routes ------------------------------- */

var (
	// ginGroupPattern matches `api := r.Group("/api/v1")` and
	// `v1 := api.Group("/v1", middleware.Auth())` (extra arguments allowed).
	ginGroupPattern = regexp.MustCompile(`(?m)^\s*([A-Za-z_][A-Za-z0-9_]*)\s*(?::=|=)\s*([A-Za-z_][A-Za-z0-9_]*)\.Group\(\s*["\x60]([^"\x60\n]+)["\x60][^)\n]*\)`)
	// ginRoutePattern matches `api.GET("/orders/:id", handler)`.
	ginRoutePattern = regexp.MustCompile(`(?m)^\s*([A-Za-z_][A-Za-z0-9_]*)\.(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|Any)\(\s*["\x60]([^"\x60\n]+)["\x60]`)
	// ginHandlePattern matches `r.Handle("GET", "/path", handler)`.
	ginHandlePattern = regexp.MustCompile(`(?m)^\s*([A-Za-z_][A-Za-z0-9_]*)\.Handle\(\s*"(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)"\s*,\s*"([^"\n]+)"`)
)

type ginGroup struct {
	receiver string
	prefix   string
}

func extractGinRoutes(files []SourceFile) []StaticAPI {
	apis := make([]StaticAPI, 0)
	for _, file := range files {
		lowerPath := strings.ToLower(file.Path)
		if !isGoSourceFile(lowerPath) || strings.HasSuffix(lowerPath, "_test.go") {
			continue
		}
		groups := map[string]ginGroup{}
		for _, match := range ginGroupPattern.FindAllStringSubmatch(file.Content, -1) {
			groups[match[1]] = ginGroup{receiver: match[2], prefix: match[3]}
		}
		var prefixOf func(string, map[string]bool) string
		prefixOf = func(name string, seen map[string]bool) string {
			group, ok := groups[name]
			if !ok || seen[name] {
				return ""
			}
			seen[name] = true
			parent := prefixOf(group.receiver, seen)
			return joinAPIPath(parent, group.prefix)
		}

		add := func(receiver, method, rawPath string, offset int) {
			prefix := prefixOf(receiver, map[string]bool{})
			full := joinAPIPath(prefix, rawPath)
			templatePath, params := normalizeStaticAPIPath(full)
			if templatePath == "" {
				return
			}
			for _, expanded := range expandGinMethod(method) {
				apis = append(apis, StaticAPI{
					Method:       expanded,
					PathTemplate: templatePath,
					Params:       uniqueSortedStrings(params),
					Source:       StaticSourceGinRoute,
					File:         file.Path,
					Line:         lineNumberAt(file.Content, offset),
				})
			}
		}
		for _, match := range ginRoutePattern.FindAllStringSubmatchIndex(file.Content, -1) {
			add(file.Content[match[2]:match[3]], file.Content[match[4]:match[5]], file.Content[match[6]:match[7]], match[0])
		}
		for _, match := range ginHandlePattern.FindAllStringSubmatchIndex(file.Content, -1) {
			add(file.Content[match[2]:match[3]], file.Content[match[4]:match[5]], file.Content[match[6]:match[7]], match[0])
		}
	}
	return apis
}

func expandGinMethod(method string) []string {
	if !strings.EqualFold(method, "Any") {
		return []string{strings.ToUpper(method)}
	}
	return []string{"DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT"}
}

func joinAPIPath(prefix, path string) string {
	joined := strings.TrimRight(strings.TrimSpace(prefix), "/") + "/" + strings.TrimLeft(strings.TrimSpace(path), "/")
	joined = strings.ReplaceAll(joined, "//", "/")
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	return joined
}

/* ---------------------------- OpenAPI / Swagger --------------------------- */

type openAPISecurityScheme struct {
	Type   string `json:"type"`
	Scheme string `json:"scheme"`
	Name   string `json:"name"`
	In     string `json:"in"`
}

type openAPIParameter struct {
	Name string `json:"name"`
	In   string `json:"in"`
}

type openAPIOperation struct {
	OperationID string                 `json:"operationId"`
	Summary     string                 `json:"summary"`
	Security    *[]map[string][]string `json:"security"`
	Parameters  []openAPIParameter     `json:"parameters"`
}

type openAPIJSONDocument struct {
	OpenAPI    string                                `json:"openapi"`
	Swagger    string                                `json:"swagger"`
	Security   []map[string][]string                 `json:"security"`
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		SecuritySchemes map[string]openAPISecurityScheme `json:"securitySchemes"`
	} `json:"components"`
	SecurityDefinitions map[string]openAPISecurityScheme `json:"securityDefinitions"`
}

func extractOpenAPIDocuments(files []SourceFile) []StaticAPI {
	apis := make([]StaticAPI, 0)
	for _, file := range files {
		if !isOpenAPISource(file.Path, file.Content) {
			continue
		}
		trimmed := strings.TrimSpace(file.Content)
		if strings.HasPrefix(trimmed, "{") {
			apis = append(apis, parseOpenAPIJSON(file)...)
			continue
		}
		apis = append(apis, parseOpenAPIYAML(file)...)
	}
	return apis
}

func isOpenAPISource(path, content string) bool {
	lowerPath := strings.ToLower(strings.TrimSpace(path))
	extension := lowerPath
	if dot := strings.LastIndex(lowerPath, "."); dot >= 0 {
		extension = lowerPath[dot:]
	}
	if extension != ".json" && extension != ".yaml" && extension != ".yml" {
		return false
	}
	if strings.Contains(lowerPath, "openapi") || strings.Contains(lowerPath, "swagger") {
		return true
	}
	lowerContent := strings.ToLower(content)
	return strings.Contains(lowerContent, `"openapi"`) ||
		strings.Contains(lowerContent, `"swagger"`) ||
		strings.Contains(lowerContent, "openapi:") ||
		strings.Contains(lowerContent, "swagger:")
}

func parseOpenAPIJSON(file SourceFile) []StaticAPI {
	var document openAPIJSONDocument
	if err := json.Unmarshal([]byte(file.Content), &document); err != nil {
		return nil
	}
	if len(document.Paths) == 0 {
		return nil
	}
	paths := make([]string, 0, len(document.Paths))
	for path := range document.Paths {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	apis := make([]StaticAPI, 0)
	for _, path := range paths {
		item := document.Paths[path]
		methods := make([]string, 0, len(item))
		for rawMethod := range item {
			method := strings.ToLower(strings.TrimSpace(rawMethod))
			if isHTTPMethod(method) {
				methods = append(methods, method)
			}
		}
		sort.Strings(methods)
		pathTemplate, pathParams := normalizeStaticAPIPath(path)
		if pathTemplate == "" {
			continue
		}
		for _, method := range methods {
			var operation openAPIOperation
			_ = json.Unmarshal(item[method], &operation)
			params := append([]string(nil), pathParams...)
			for _, parameter := range operation.Parameters {
				switch strings.ToLower(strings.TrimSpace(parameter.In)) {
				case "path", "query", "header":
					if name := strings.TrimSpace(parameter.Name); name != "" {
						params = append(params, name)
					}
				}
			}
			apis = append(apis, StaticAPI{
				Method:       strings.ToUpper(method),
				PathTemplate: pathTemplate,
				Params:       uniqueSortedStrings(params),
				Source:       StaticSourceOpenAPI,
				File:         file.Path,
				Line:         jsonPathLine(file.Content, path),
				Name:         firstNonEmpty(operation.OperationID, operation.Summary),
				Auth:         openAPIAuth(operation.Security, document),
			})
		}
	}
	return apis
}

func openAPIAuth(operationSecurity *[]map[string][]string, document openAPIJSONDocument) string {
	security := document.Security
	if operationSecurity != nil {
		security = *operationSecurity
		if len(security) == 0 {
			return ""
		}
	}
	if len(security) == 0 {
		return ""
	}
	schemes := make([]string, 0)
	for _, requirement := range security {
		for name := range requirement {
			if name = strings.TrimSpace(name); name != "" {
				schemes = append(schemes, name)
			}
		}
	}
	sort.Strings(schemes)
	for _, name := range schemes {
		if definition, ok := document.Components.SecuritySchemes[name]; ok {
			return authFromOpenAPIScheme(definition)
		}
		if definition, ok := document.SecurityDefinitions[name]; ok {
			return authFromOpenAPIScheme(definition)
		}
	}
	if len(schemes) > 0 {
		return authFromSchemeName(schemes[0])
	}
	return ""
}

func authFromOpenAPIScheme(definition openAPISecurityScheme) string {
	switch strings.ToLower(strings.TrimSpace(definition.Type)) {
	case "http":
		switch strings.ToLower(strings.TrimSpace(definition.Scheme)) {
		case "bearer":
			return "bearer"
		case "basic":
			return "basic"
		}
	case "apikey":
		return "api_key"
	case "oauth2", "openidconnect":
		return "oauth2"
	}
	return authFromSchemeName(strings.TrimSpace(definition.Scheme + " " + definition.Name))
}

func authFromSchemeName(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "bearer"), strings.Contains(lower, "jwt"):
		return "bearer"
	case strings.Contains(lower, "basic"):
		return "basic"
	case strings.Contains(lower, "apikey"), strings.Contains(lower, "api_key"), strings.Contains(lower, "api-key"):
		return "api_key"
	case strings.Contains(lower, "oauth"):
		return "oauth2"
	case strings.TrimSpace(name) == "":
		return ""
	default:
		return "required"
	}
}

func parseOpenAPIYAML(file SourceFile) []StaticAPI {
	lines := strings.Split(file.Content, "\n")
	inPaths := false
	currentPath := ""
	apis := make([]StaticAPI, 0)
	for index, rawLine := range lines {
		line := strings.TrimRight(rawLine, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := leadingSpaces(line)
		if !inPaths {
			if trimmed == "paths:" || trimmed == `"paths":` {
				inPaths = true
			}
			continue
		}
		if indent == 0 {
			break
		}
		if indent == 2 {
			if key := yamlMapKey(trimmed); strings.HasPrefix(key, "/") {
				currentPath = key
				continue
			}
		}
		if currentPath == "" || indent < 4 {
			continue
		}
		method := strings.ToLower(yamlMapKey(trimmed))
		if !isHTTPMethod(method) || !strings.HasSuffix(trimmed, ":") {
			continue
		}
		pathTemplate, pathParams := normalizeStaticAPIPath(currentPath)
		if pathTemplate == "" {
			continue
		}
		auth, name := scanYAMLOperation(lines, index+1, indent)
		apis = append(apis, StaticAPI{
			Method:       strings.ToUpper(method),
			PathTemplate: pathTemplate,
			Params:       uniqueSortedStrings(pathParams),
			Source:       StaticSourceOpenAPI,
			File:         file.Path,
			Line:         index + 1,
			Name:         name,
			Auth:         auth,
		})
	}
	return apis
}

// scanYAMLOperation reads the operation body after a method key for its
// operationId and security requirement names. It stops at the next method key
// (same indent) or at the end of the paths block.
func scanYAMLOperation(lines []string, start, methodIndent int) (auth, name string) {
	securityActive := false
	schemes := make([]string, 0)
	for index := start; index < len(lines); index++ {
		line := strings.TrimRight(lines[index], "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := leadingSpaces(line)
		if indent <= methodIndent {
			break
		}
		if strings.HasPrefix(trimmed, "operationId:") {
			name = yamlScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "operationId:")))
			continue
		}
		if strings.HasPrefix(trimmed, "security:") {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "security:"))
			securityActive = rest != "[]"
			continue
		}
		if securityActive && strings.HasPrefix(trimmed, "- ") {
			scheme := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			if cut := strings.IndexByte(scheme, ':'); cut >= 0 {
				scheme = scheme[:cut]
			}
			scheme = strings.TrimSpace(strings.Trim(scheme, `"':`))
			if scheme != "" {
				schemes = append(schemes, scheme)
			}
		}
	}
	if len(schemes) > 0 {
		sort.Strings(schemes)
		return authFromSchemeName(schemes[0]), name
	}
	if securityActive {
		return "required", name
	}
	return "", name
}

func yamlMapKey(trimmed string) string {
	key := trimmed
	if cut := strings.IndexByte(key, ':'); cut >= 0 {
		key = key[:cut]
	}
	key = strings.TrimSpace(key)
	key = strings.Trim(key, `"'`)
	return key
}

func yamlScalar(value string) string {
	return strings.TrimSpace(strings.Trim(value, `"'`))
}

/* --------------------------- frontend api-client -------------------------- */

var (
	// apiClientCallPattern covers `axios.get(...)`, `api.post(...)`,
	// `apiClient.get<T>(...)` (the generic argument is skipped) and similar
	// api-client wrappers. It captures the quote character so a template
	// literal path is accepted.
	apiClientCallPattern = regexp.MustCompile("\\b(?:axios|api|client|http|request|apiClient|httpClient)\\s*\\.\\s*(get|post|put|patch|delete|head|options)\\s*(?:<[^>\\n]*>)?\\s*\\(\\s*([\\x60'\"])([^\\x60'\"\\n]+)([\\x60'\"])")
	// fetchCallPattern covers the platform fetch API; its method is read from
	// the options object when present, defaulting to GET.
	fetchCallPattern = regexp.MustCompile("\\bfetch\\s*\\(\\s*([\\x60'\"])([^\\x60'\"\\n]+)([\\x60'\"])")
	// requestMethodPattern finds `method: 'POST'` in a request options object.
	requestMethodPattern = regexp.MustCompile(`(?i)\b(?:method|httpMethod)\s*:\s*['"]([A-Za-z]+)['"]`)
	// requestURLPattern finds `url: '/api/orders'` in a request options object.
	requestURLPattern = regexp.MustCompile("\\burl\\s*:\\s*([\\x60'\"])([^\\x60'\"\\n]+)([\\x60'\"])")
	// baseURLPattern finds the axios/ky client base path so relative calls merge
	// with the backend's absolute route declarations.
	baseURLPattern = regexp.MustCompile("(?i)\\bbaseUrl\\s*:\\s*([\\x60'\"])([^\\x60'\"\\n]+)[\\x60'\"]")
	// generatedTypeHeader marks an OpenAPI-generated TypeScript paths interface.
	generatedTypeHeader = regexp.MustCompile(`(?m)(?:export\s+)?(?:interface|type)\s+paths\b`)
)

func extractFrontendAPIClients(files []SourceFile) []StaticAPI {
	apis := make([]StaticAPI, 0)
	for _, file := range files {
		if !isFrontendSourceFile(strings.ToLower(file.Path)) {
			continue
		}
		apis = append(apis, extractAPIClientCalls(file)...)
		apis = append(apis, extractRequestOptions(file)...)
	}
	return apis
}

func extractAPIClientCalls(file SourceFile) []StaticAPI {
	apis := make([]StaticAPI, 0)
	basePath := frontendBasePath(file.Content)
	for _, match := range apiClientCallPattern.FindAllStringSubmatchIndex(file.Content, -1) {
		method := strings.ToUpper(file.Content[match[2]:match[3]])
		rawPath := file.Content[match[6]:match[7]]
		templatePath, params := normalizeStaticAPIPath(applyBasePath(rawPath, basePath))
		if templatePath == "" {
			continue
		}
		apis = append(apis, StaticAPI{
			Method:       method,
			PathTemplate: templatePath,
			Params:       uniqueSortedStrings(params),
			Source:       StaticSourceAPIClient,
			File:         file.Path,
			Line:         lineNumberAt(file.Content, match[0]),
		})
	}
	for _, match := range fetchCallPattern.FindAllStringSubmatchIndex(file.Content, -1) {
		rawPath := file.Content[match[4]:match[5]]
		templatePath, params := normalizeStaticAPIPath(rawPath)
		if templatePath == "" {
			continue
		}
		method := "GET"
		if methodMatch := requestMethodPattern.FindStringSubmatch(file.Content[match[1]:minInt(len(file.Content), match[1]+300)]); len(methodMatch) > 1 {
			candidate := strings.ToUpper(strings.TrimSpace(methodMatch[1]))
			if isHTTPMethod(strings.ToLower(candidate)) {
				method = candidate
			}
		}
		apis = append(apis, StaticAPI{
			Method:       method,
			PathTemplate: templatePath,
			Params:       uniqueSortedStrings(params),
			Source:       StaticSourceAPIClient,
			File:         file.Path,
			Line:         lineNumberAt(file.Content, match[0]),
		})
	}
	return apis
}

// extractRequestOptions covers `request({ url: '/api/x', method: 'POST' })`
// style clients whose wrapper method is not one of the HTTP verbs.
func extractRequestOptions(file SourceFile) []StaticAPI {
	apis := make([]StaticAPI, 0)
	basePath := frontendBasePath(file.Content)
	for _, methodMatch := range requestMethodPattern.FindAllStringSubmatchIndex(file.Content, -1) {
		method := strings.ToUpper(file.Content[methodMatch[2]:methodMatch[3]])
		if !isHTTPMethod(strings.ToLower(method)) {
			continue
		}
		windowStart := maxInt(0, methodMatch[0]-240)
		windowEnd := minInt(len(file.Content), methodMatch[1]+240)
		urlMatch := requestURLPattern.FindStringSubmatchIndex(file.Content[windowStart:windowEnd])
		if urlMatch == nil {
			continue
		}
		rawPath := file.Content[windowStart+urlMatch[4] : windowStart+urlMatch[5]]
		templatePath, params := normalizeStaticAPIPath(applyBasePath(rawPath, basePath))
		if templatePath == "" {
			continue
		}
		apis = append(apis, StaticAPI{
			Method:       method,
			PathTemplate: templatePath,
			Params:       uniqueSortedStrings(params),
			Source:       StaticSourceAPIClient,
			File:         file.Path,
			Line:         lineNumberAt(file.Content, methodMatch[0]),
		})
	}
	return apis
}

// frontendBasePath extracts the client base path (e.g. axios `baseURL: '/api'`)
// so relative call sites merge with the backend's absolute route declarations.
// It is a best-effort per-file heuristic: a base URL that is assembled at
// runtime cannot be resolved statically.
func frontendBasePath(content string) string {
	match := baseURLPattern.FindStringSubmatch(content)
	if len(match) < 3 {
		return ""
	}
	base, _ := normalizeStaticAPIPath(match[2])
	return base
}

// applyBasePath prefixes a relative api-client path with the file's base path,
// avoiding a double prefix when the call site already uses the full path.
func applyBasePath(rawPath, basePath string) string {
	rawPath = strings.TrimSpace(rawPath)
	if basePath == "" || basePath == "/" || rawPath == "" {
		return rawPath
	}
	lower := strings.ToLower(rawPath)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(rawPath, "${") {
		return rawPath
	}
	if !strings.HasPrefix(rawPath, "/") {
		rawPath = "/" + rawPath
	}
	base := strings.TrimRight(basePath, "/")
	if rawPath == base || strings.HasPrefix(rawPath, base+"/") {
		return rawPath
	}
	return base + rawPath
}

/* ------------------------- generated type definitions --------------------- */
func isGeneratedTypeFile(content string) bool {
	return generatedTypeHeader.MatchString(content)
}

func extractAPITypeDefinitions(files []SourceFile) []StaticAPI {
	apis := make([]StaticAPI, 0)
	for _, file := range files {
		if !isGeneratedTypeFile(file.Content) {
			continue
		}
		lines := strings.Split(file.Content, "\n")
		for index, rawLine := range lines {
			line := strings.TrimRight(rawLine, "\r")
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, `"`) && !strings.HasPrefix(trimmed, "'") {
				continue
			}
			quote := trimmed[0]
			end := strings.IndexByte(trimmed[1:], quote)
			if end < 0 {
				continue
			}
			rawPath := trimmed[1 : 1+end]
			rest := strings.TrimSpace(trimmed[1+end+1:])
			if !strings.HasPrefix(rawPath, "/") || !strings.HasPrefix(rest, ":") {
				continue
			}
			templatePath, params := normalizeStaticAPIPath(rawPath)
			if templatePath == "" {
				continue
			}
			baseIndent := leadingSpaces(line)
			for next := index + 1; next < len(lines); next++ {
				nextLine := strings.TrimRight(lines[next], "\r")
				nextTrimmed := strings.TrimSpace(nextLine)
				if nextTrimmed == "" || strings.HasPrefix(nextTrimmed, "//") {
					continue
				}
				nextIndent := leadingSpaces(nextLine)
				if nextIndent <= baseIndent && (strings.HasPrefix(nextTrimmed, "}") ||
					strings.HasPrefix(nextTrimmed, `"`) || strings.HasPrefix(nextTrimmed, "'")) {
					break
				}
				key := nextTrimmed
				if cut := strings.IndexByte(key, ':'); cut >= 0 {
					key = key[:cut]
				}
				method := strings.ToLower(strings.TrimSpace(key))
				if !isHTTPMethod(method) {
					continue
				}
				apis = append(apis, StaticAPI{
					Method:       strings.ToUpper(method),
					PathTemplate: templatePath,
					Params:       uniqueSortedStrings(params),
					Source:       StaticSourceTypeDefinition,
					File:         file.Path,
					Line:         next + 1,
				})
			}
		}
	}
	return apis
}

/* ------------------------------ normalization ----------------------------- */

var templateExpressionPattern = regexp.MustCompile(`\$\{[^}]*\}`)

// normalizeStaticAPIPath canonicalizes one declared or observed path to the
// shared coverage identity: scheme/host/query/fragment are dropped, every
// dynamic segment (`:id`, `*rest`, `{id}`, `${id}`) becomes `{id}`, and concrete
// numeric/UUID/long-hex segments become `{id}`. It returns the parameter names
// seen in the source, in encounter order.
func normalizeStaticAPIPath(raw string) (string, []string) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "${") {
		if close := strings.IndexByte(value, '}'); close >= 0 && close+1 < len(value) && value[close+1] == '/' {
			value = value[close+1:]
		}
	}
	value = dropURLQueryAndFragment(value)
	if value == "" || strings.ContainsAny(value, "\\\x00\r\n\t") {
		return "", nil
	}
	params := make([]string, 0)

	segments := strings.Split(value, "/")
	normalizedSegments := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == "" || segment == "." {
			continue
		}
		if segment == ".." {
			return "", nil
		}
		if name, ok := dynamicSegmentName(segment); ok {
			if name != "" {
				params = append(params, name)
			}
			normalizedSegments = append(normalizedSegments, "{id}")
			continue
		}
		if strings.Contains(segment, "${") {
			segment = templateExpressionPattern.ReplaceAllStringFunc(segment, func(match string) string {
				name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(match, "${"), "}"))
				if name != "" {
					params = append(params, name)
				}
				return "{id}"
			})
		}
		normalizedSegments = append(normalizedSegments, segment)
	}
	prepared := "/" + strings.Join(normalizedSegments, "/")
	normalized := codeintel.NormalizeAPIPath(prepared)
	if normalized == "" || len([]rune(normalized)) > maxStaticPathChars {
		return "", nil
	}
	return normalized, uniqueSortedStrings(params)
}

func dynamicSegmentName(segment string) (string, bool) {
	switch {
	case strings.HasPrefix(segment, ":") || strings.HasPrefix(segment, "*"):
		name := strings.TrimSpace(segment[1:])
		if name == "" || strings.ContainsAny(name, "{}") {
			return "", false
		}
		return name, true
	case strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") && len(segment) > 2:
		return strings.TrimSpace(segment[1 : len(segment)-1]), true
	}
	return "", false
}

func dropURLQueryAndFragment(value string) string {
	if scheme := strings.Index(value, "://"); scheme >= 0 {
		rest := value[scheme+3:]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			value = rest[slash:]
		} else {
			value = "/"
		}
	}
	if cut := strings.IndexAny(value, "?#"); cut >= 0 {
		value = value[:cut]
	}
	return value
}

/* --------------------------------- helpers -------------------------------- */

func isGoSourceFile(lowerPath string) bool {
	return strings.HasSuffix(lowerPath, ".go") &&
		!strings.Contains(lowerPath, "vendor/") &&
		!strings.Contains(lowerPath, "node_modules/")
}

func isFrontendSourceFile(lowerPath string) bool {
	switch {
	case strings.Contains(lowerPath, "node_modules/"),
		strings.Contains(lowerPath, "/dist/"),
		strings.Contains(lowerPath, "/build/"),
		strings.Contains(lowerPath, "__tests__/"),
		strings.Contains(lowerPath, "__mocks__/"):
		return false
	}
	for _, suffix := range []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"} {
		if strings.HasSuffix(lowerPath, suffix) {
			if strings.Contains(lowerPath, ".test.") || strings.Contains(lowerPath, ".spec.") {
				return false
			}
			return true
		}
	}
	return false
}

func isHTTPMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return true
	default:
		return false
	}
}

func mergeStaticAPI(destination map[string]StaticAPI, api StaticAPI) {
	if api.Method == "" || api.PathTemplate == "" || api.Source == "" {
		return
	}
	key := api.Source + "|" + api.Method + " " + api.PathTemplate
	existing, ok := destination[key]
	if !ok {
		api.Params = uniqueSortedStrings(api.Params)
		destination[key] = api
		return
	}
	existing.Params = uniqueSortedStrings(append(existing.Params, api.Params...))
	if existing.Auth == "" {
		existing.Auth = api.Auth
	}
	if existing.Name == "" {
		existing.Name = api.Name
	}
	destination[key] = existing
}

func sortedStaticAPIs(source map[string]StaticAPI) []StaticAPI {
	apis := make([]StaticAPI, 0, len(source))
	for _, api := range source {
		apis = append(apis, api)
	}
	sort.Slice(apis, func(i, j int) bool {
		if apis[i].Method != apis[j].Method {
			return apis[i].Method < apis[j].Method
		}
		if apis[i].PathTemplate != apis[j].PathTemplate {
			return apis[i].PathTemplate < apis[j].PathTemplate
		}
		if apis[i].Source != apis[j].Source {
			return apis[i].Source < apis[j].Source
		}
		if apis[i].File != apis[j].File {
			return apis[i].File < apis[j].File
		}
		return apis[i].Line < apis[j].Line
	})
	return apis
}

func uniqueSortedStrings(values []string) []string {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			unique[trimmed] = struct{}{}
		}
	}
	out := make([]string, 0, len(unique))
	for value := range unique {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func positiveOrDefault(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func leadingSpaces(line string) int {
	count := 0
	for _, r := range line {
		if r != ' ' && r != '\t' {
			break
		}
		count++
	}
	return count
}

func lineNumberAt(content string, offset int) int {
	if offset <= 0 {
		return 1
	}
	if offset > len(content) {
		offset = len(content)
	}
	return strings.Count(content[:offset], "\n") + 1
}

func jsonPathLine(content, path string) int {
	if index := strings.Index(content, `"`+path+`"`); index >= 0 {
		return strings.Count(content[:index], "\n") + 1
	}
	return 1
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

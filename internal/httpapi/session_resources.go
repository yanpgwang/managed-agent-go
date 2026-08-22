package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/yanpgwang/mango/internal/app"
	"github.com/yanpgwang/mango/internal/domain"
)

func sessionResourceToJSON(resource domain.SessionResource) map[string]any {
	if resource.Type() == domain.SessionResourceTypeMemoryStore {
		var instructions any
		if resource.MemoryInstructions != "" {
			instructions = resource.MemoryInstructions
		}
		return map[string]any{
			"memory_store_id": resource.MemoryStoreID,
			"type":            domain.SessionResourceTypeMemoryStore,
			"access":          resource.MemoryAccess,
			"description":     resource.MemoryStoreDescription,
			"instructions":    instructions,
			"mount_path":      resource.MountPath,
			"name":            resource.MemoryStoreName,
		}
	}
	if resource.Type() == domain.SessionResourceTypeGitRepository {
		var checkout any
		switch resource.RepositoryCheckoutType {
		case domain.GitRepositoryCheckoutBranch:
			checkout = map[string]any{
				"type": domain.GitRepositoryCheckoutBranch,
				"name": resource.RepositoryCheckoutValue,
			}
		case domain.GitRepositoryCheckoutCommit:
			checkout = map[string]any{
				"type": domain.GitRepositoryCheckoutCommit,
				"sha":  resource.RepositoryCheckoutValue,
			}
		}
		return map[string]any{
			"id":              resource.ID,
			"created_at":      resource.CreatedAt.Format(timeFmt),
			"type":            domain.SessionResourceTypeGitRepository,
			"url":             resource.RepositoryURL,
			"checkout":        checkout,
			"resolved_commit": resource.RepositoryResolvedCommit,
			"mount_path":      resource.MountPath,
			"updated_at":      resource.UpdatedAt.Format(timeFmt),
		}
	}
	return map[string]any{
		"id":         resource.ID,
		"created_at": resource.CreatedAt.Format(timeFmt),
		"file_id":    resource.FileID,
		"mount_path": resource.MountPath,
		"type":       "file",
		"updated_at": resource.UpdatedAt.Format(timeFmt),
	}
}

func parseSessionResourceInputs(
	raw *[]json.RawMessage,
) ([]app.FileSessionResourceInput, []app.MemorySessionResourceInput, []app.GitRepositorySessionResourceInput, error) {
	if raw == nil || len(*raw) == 0 {
		return nil, nil, nil, nil
	}
	if len(*raw) > 500 {
		return nil, nil, nil, domain.Validation("resources must contain at most 500 entries")
	}
	files := make([]app.FileSessionResourceInput, 0, len(*raw))
	memories := make([]app.MemorySessionResourceInput, 0, len(*raw))
	repositories := make([]app.GitRepositorySessionResourceInput, 0, len(*raw))
	for _, item := range *raw {
		resourceType, err := parseSessionResourceType(item)
		if err != nil {
			return nil, nil, nil, err
		}
		switch resourceType {
		case domain.SessionResourceTypeFile:
			input, err := parseSessionFileResourceInput(item)
			if err != nil {
				return nil, nil, nil, err
			}
			files = append(files, input)
		case domain.SessionResourceTypeMemoryStore:
			input, err := parseSessionMemoryResourceInput(item)
			if err != nil {
				return nil, nil, nil, err
			}
			memories = append(memories, input)
		case domain.SessionResourceTypeGitRepository:
			input, err := parseSessionGitRepositoryResourceInput(item)
			if err != nil {
				return nil, nil, nil, err
			}
			repositories = append(repositories, input)
		default:
			return nil, nil, nil, domain.Unsupported("unsupported Session Resource type")
		}
	}
	if len(memories) > domain.MaxSessionMemoryStores {
		return nil, nil, nil, domain.Validation("resources may contain at most 8 Memory Stores")
	}
	return files, memories, repositories, nil
}

func parseSessionResourceType(raw json.RawMessage) (string, error) {
	var discriminator struct {
		Type string `json:"type"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&discriminator); err != nil {
		return "", domain.Validation(
			"session resource must be a valid resource object",
		)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return "", domain.Validation(
			"session resource must contain exactly one JSON object",
		)
	}
	if discriminator.Type == "" {
		return "", domain.Validation(
			"session resource type is required",
		)
	}
	return discriminator.Type, nil
}

func parseSessionFileResourceInput(raw json.RawMessage) (app.FileSessionResourceInput, error) {
	resourceType, err := parseSessionResourceType(raw)
	if err != nil {
		return app.FileSessionResourceInput{}, err
	}
	if resourceType != domain.SessionResourceTypeFile {
		return app.FileSessionResourceInput{}, domain.Unsupported(
			"this resource type can be attached only while creating a Session",
		)
	}
	var input struct {
		Type      string  `json:"type"`
		FileID    string  `json:"file_id"`
		MountPath *string `json:"mount_path"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return app.FileSessionResourceInput{}, domain.Validation(
			"session resource must be a valid resource object",
		)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return app.FileSessionResourceInput{}, domain.Validation(
			"session resource must contain exactly one JSON object",
		)
	}
	if input.FileID == "" {
		return app.FileSessionResourceInput{}, domain.Validation("file_id is required")
	}
	return app.FileSessionResourceInput{
		FileID: input.FileID, MountPath: input.MountPath,
	}, nil
}

func parseSessionMemoryResourceInput(raw json.RawMessage) (app.MemorySessionResourceInput, error) {
	var input struct {
		Type          string                    `json:"type"`
		MemoryStoreID string                    `json:"memory_store_id"`
		Access        optionalJSONField[string] `json:"access"`
		Instructions  optionalJSONField[string] `json:"instructions"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return app.MemorySessionResourceInput{}, domain.Validation(
			"session resource must be a valid Memory Store resource object",
		)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return app.MemorySessionResourceInput{}, domain.Validation(
			"session resource must contain exactly one JSON object",
		)
	}
	if input.Type != domain.SessionResourceTypeMemoryStore {
		return app.MemorySessionResourceInput{}, domain.Validation(
			"session resource type must be memory_store",
		)
	}
	if input.MemoryStoreID == "" {
		return app.MemorySessionResourceInput{}, domain.Validation("memory_store_id is required")
	}
	if input.Access.Null || input.Instructions.Null {
		return app.MemorySessionResourceInput{}, domain.Validation(
			"access and instructions cannot be null",
		)
	}
	access := domain.MemoryAccessReadWrite
	if input.Access.Present {
		access = input.Access.Value
	}
	if access != domain.MemoryAccessReadWrite && access != domain.MemoryAccessReadOnly {
		return app.MemorySessionResourceInput{}, domain.Validation(
			"access must be read_write or read_only",
		)
	}
	instructions := ""
	if input.Instructions.Present {
		instructions = input.Instructions.Value
	}
	return app.MemorySessionResourceInput{
		MemoryStoreID: input.MemoryStoreID,
		Access:        access,
		Instructions:  instructions,
	}, nil
}

func parseSessionGitRepositoryResourceInput(
	raw json.RawMessage,
) (app.GitRepositorySessionResourceInput, error) {
	var input struct {
		Type      string          `json:"type"`
		URL       string          `json:"url"`
		Checkout  json.RawMessage `json:"checkout"`
		MountPath *string         `json:"mount_path"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return app.GitRepositorySessionResourceInput{}, domain.Validation(
			"session resource must be a valid Git repository resource object",
		)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return app.GitRepositorySessionResourceInput{}, domain.Validation(
			"session resource must contain exactly one JSON object",
		)
	}
	if input.Type != domain.SessionResourceTypeGitRepository {
		return app.GitRepositorySessionResourceInput{}, domain.Validation(
			"session resource type must be git_repository",
		)
	}
	if err := domain.ValidateGitRepositoryURL(input.URL); err != nil {
		return app.GitRepositorySessionResourceInput{}, err
	}
	var checkout *app.GitRepositoryCheckoutInput
	if string(input.Checkout) == "null" {
		return app.GitRepositorySessionResourceInput{}, domain.Validation(
			"checkout cannot be null",
		)
	}
	if len(input.Checkout) > 0 {
		parsed, err := parseGitRepositoryCheckout(input.Checkout)
		if err != nil {
			return app.GitRepositorySessionResourceInput{}, err
		}
		checkout = &parsed
	}
	if _, err := domain.NormalizeGitRepositoryMountPath(input.URL, input.MountPath); err != nil {
		return app.GitRepositorySessionResourceInput{}, err
	}
	return app.GitRepositorySessionResourceInput{
		URL: input.URL, Checkout: checkout, MountPath: input.MountPath,
	}, nil
}

func parseGitRepositoryCheckout(raw json.RawMessage) (app.GitRepositoryCheckoutInput, error) {
	var discriminator struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &discriminator); err != nil {
		return app.GitRepositoryCheckoutInput{}, domain.Validation(
			"checkout must be a valid object",
		)
	}
	var value string
	switch discriminator.Type {
	case domain.GitRepositoryCheckoutBranch:
		var branch struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&branch); err != nil {
			return app.GitRepositoryCheckoutInput{}, domain.Validation(
				"branch checkout must contain only type and name",
			)
		}
		value = branch.Name
	case domain.GitRepositoryCheckoutCommit:
		var commit struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&commit); err != nil {
			return app.GitRepositoryCheckoutInput{}, domain.Validation(
				"commit checkout must contain only type and sha",
			)
		}
		value = commit.SHA
	default:
		return app.GitRepositoryCheckoutInput{}, domain.Validation(
			"checkout.type must be branch or commit",
		)
	}
	checkoutType, checkoutValue, err := domain.NormalizeGitRepositoryCheckout(
		discriminator.Type, value,
	)
	if err != nil {
		return app.GitRepositoryCheckoutInput{}, err
	}
	return app.GitRepositoryCheckoutInput{Type: checkoutType, Value: checkoutValue}, nil
}

func (s *Server) addSessionResource(w http.ResponseWriter, r *http.Request) {
	if s.deps.SessionResources == nil {
		writeError(w, domain.Unsupported(
			"File resources are unavailable for the configured deployment",
		))
		return
	}
	var raw json.RawMessage
	if err := decodeJSONBody(r, &raw); err != nil {
		writeError(w, err)
		return
	}
	input, err := parseSessionFileResourceInput(raw)
	if err != nil {
		writeError(w, err)
		return
	}
	resource, err := s.deps.SessionResources.Add(r.Context(), r.PathValue("id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionResourceToJSON(resource))
}

func (s *Server) getSessionResource(w http.ResponseWriter, r *http.Request) {
	if s.deps.SessionResources == nil {
		writeError(w, domain.Unsupported(
			"File resources are unavailable for the configured deployment",
		))
		return
	}
	resource, err := s.deps.SessionResources.Get(
		r.Context(), r.PathValue("id"), r.PathValue("resource_id"),
	)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionResourceToJSON(resource))
}

func (s *Server) listSessionResources(w http.ResponseWriter, r *http.Request) {
	if s.deps.SessionResources == nil {
		writeError(w, domain.Unsupported(
			"File resources are unavailable for the configured deployment",
		))
		return
	}
	query := app.SessionResourceListQuery{}
	values := r.URL.Query()
	if values.Has("limit") {
		limit, err := strconv.Atoi(values.Get("limit"))
		if err != nil || limit < 1 || limit > 1000 {
			writeError(w, domain.Validation("limit must be between 1 and 1000"))
			return
		}
		query.Limit = limit
	}
	filter := sessionResourceCursorFilter{SessionID: r.PathValue("id")}
	if values.Has("page") {
		cursor, ok := decodeResourceCursor(
			values.Get("page"), sessionResourceListCursorKind,
		)
		if !ok || cursor.Filter != filter.fingerprint() {
			writeError(w, domain.Validation("invalid page cursor"))
			return
		}
		createdAt, ok := parseTimeParam(cursor.CreatedAt)
		if !ok {
			writeError(w, domain.Validation("invalid page cursor"))
			return
		}
		query.Boundary = &app.SessionResourcePageBoundary{
			CreatedAt: createdAt.UTC(), ID: cursor.ID,
		}
	}
	page, err := s.deps.SessionResources.List(r.Context(), r.PathValue("id"), query)
	if err != nil {
		writeError(w, err)
		return
	}
	data := make([]any, 0, len(page.Resources))
	for _, resource := range page.Resources {
		data = append(data, sessionResourceToJSON(resource))
	}
	var nextPage any
	if page.HasMore && len(page.Resources) > 0 {
		last := page.Resources[len(page.Resources)-1]
		nextPage = encodeResourceCursor(resourceCursor{
			Kind: sessionResourceListCursorKind, CreatedAt: last.CreatedAt.Format(timeFmt),
			ID: last.ID, Filter: filter.fingerprint(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": data, "has_more": page.HasMore, "next_page": nextPage,
	})
}

func (s *Server) deleteSessionResource(w http.ResponseWriter, r *http.Request) {
	if s.deps.SessionResources == nil {
		writeError(w, domain.Unsupported(
			"File resources are unavailable for the configured deployment",
		))
		return
	}
	resource, err := s.deps.SessionResources.Delete(
		r.Context(), r.PathValue("id"), r.PathValue("resource_id"),
	)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": resource.ID, "type": "session_resource_deleted",
	})
}

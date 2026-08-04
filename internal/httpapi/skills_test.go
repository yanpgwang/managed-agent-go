package httpapi

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSkillsHTTP_RouteHeadersMultipartAndLimits(t *testing.T) {
	service := newSDKSkillService()
	strict := NewServer(Deps{Skills: service}, Config{
		RequireBeta: true, RequireAuth: true, RequireVersion: true, RequireContentType: true,
	}).Handler()
	body, contentType := skillMultipart(t, sdkSkillZip(t, "reviewing-code"), false)
	req := httptest.NewRequest(http.MethodPost, "/v1/skills", body)
	req.Header.Set("content-type", contentType)
	req.Header.Set("anthropic-beta", betaValue)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("x-api-key", "sk-test")
	rec := httptest.NewRecorder()
	strict.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "anthropic-beta") {
		t.Fatalf("Managed Agents beta on Skills route = %d: %s", rec.Code, rec.Body.String())
	}

	body, contentType = skillMultipart(t, sdkSkillZip(t, "reviewing-code"), true)
	req = httptest.NewRequest(http.MethodPost, "/v1/skills", body)
	req.Header.Set("content-type", contentType)
	req.Header.Set("anthropic-beta", skillsBetaValue)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("x-api-key", "sk-test")
	rec = httptest.NewRecorder()
	strict.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unexpected multipart part = %d: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/skills", bytes.NewReader(nil))
	req.ContentLength = maxSkillRequestBytes + 1
	rec = httptest.NewRecorder()
	NewServer(Deps{Skills: service}, Config{}).Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize Skill request = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSkillsHTTP_DisabledAndListValidation(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/skills", nil)
	rec := httptest.NewRecorder()
	NewServer(Deps{}, Config{}).Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("disabled Skills = %d: %s", rec.Code, rec.Body.String())
	}

	handler := NewServer(Deps{Skills: newSDKSkillService()}, Config{}).Handler()
	for _, target := range []string{
		"/v1/skills?source=third_party",
		"/v1/skills?limit=101",
		"/v1/skills?page=not-a-cursor",
		"/v1/skills/skill_sdk/versions?limit=1001",
	} {
		req = httptest.NewRequest(http.MethodGet, target, nil)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d: %s", target, rec.Code, rec.Body.String())
		}
	}
}

func skillMultipart(t *testing.T, archive []byte, extra bool) (*bytes.Buffer, string) {
	t.Helper()
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("files[]", "reviewing-code.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(archive); err != nil {
		t.Fatal(err)
	}
	if extra {
		if err := writer.WriteField("unexpected", "value"); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.FormDataContentType()
}

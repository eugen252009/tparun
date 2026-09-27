package main

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eugen252009/tpahub/internal/store"
)

func TestMultipartUploadFileCountLimitIsPerRequest(t *testing.T) {
	for _, tc := range []struct {
		count    int
		wantCode int
	}{
		{count: 64, wantCode: http.StatusAccepted},
		{count: 65, wantCode: http.StatusBadRequest},
		{count: 100, wantCode: http.StatusBadRequest},
	} {
		t.Run(fmt.Sprintf("%d-files", tc.count), func(t *testing.T) {
			s, _ := testServer(t)
			s.cfg.MaxUpload = 1 << 20
			s.cfg.MaxStagingBytes = 1 << 30
			s.cfg.MinDiskFreeBytes = 0
			const repositoryID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			if err := s.db.CreateRepository(context.Background(), store.Repository{ID: repositoryID, AccountID: "a", Name: "upload-batches", CreatedAt: "now"}); err != nil {
				t.Fatal(err)
			}

			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			for i := 0; i < tc.count; i++ {
				part, err := writer.CreateFormFile("files", fmt.Sprintf("package-%03d.deb", i))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = part.Write([]byte("package")); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/repositories/"+repositoryID+"/publishes", &body)
			request.Header.Set("Content-Type", writer.FormDataContentType())
			request = request.WithContext(context.WithValue(request.Context(), accountKey, "a"))
			response := httptest.NewRecorder()
			s.upload(response, request, repositoryID)
			if response.Code != tc.wantCode {
				t.Fatalf("status=%d want=%d body=%q", response.Code, tc.wantCode, response.Body.String())
			}
			if tc.count == 64 {
				if strings.Contains(response.Body.String(), "Too many packages") {
					t.Fatalf("64-file request rejected by batch limit: %s", response.Body.String())
				}
				return
			}
			message := response.Body.String()
			for _, want := range []string{"Too many packages in one upload", "64 packages per upload", "split this publication into batches", "64 packages or fewer"} {
				if !strings.Contains(strings.ToLower(message), strings.ToLower(want)) {
					t.Errorf("error message %q missing %q", message, want)
				}
			}
			if strings.Contains(strings.ToLower(message), "repository package limit") || strings.Contains(strings.ToLower(message), "repository maximum") {
				t.Fatalf("error implies a repository package cap: %q", message)
			}
		})
	}
}

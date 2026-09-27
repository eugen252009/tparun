package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPublishCLIAutomaticallyBatches64AndLargerManifests(t *testing.T) {
	for _, count := range []int{64, 65, 100, 1000, 1025} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			staging := filepath.Join(t.TempDir(), "staging")
			t.Setenv("TPARUN_STAGING_DIR", staging)
			if err := os.MkdirAll(stagingArtifactsRoot(), 0700); err != nil {
				t.Fatal(err)
			}
			deb := filepath.Join(t.TempDir(), "batch-fixture.deb")
			buildWorkerTestDeb(t, deb, "tparun-batch-fixture")
			digest, err := fileSHA256(deb)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(deb)
			if err != nil {
				t.Fatal(err)
			}
			stagedPath := filepath.Join(stagingArtifactsRoot(), digest+".deb")
			if err = os.WriteFile(stagedPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			manifest := stagingManifest{Version: 1, Artifacts: make([]stagedArtifact, count)}
			for i := range manifest.Artifacts {
				manifest.Artifacts[i] = stagedArtifact{OriginalPath: deb, StagedPath: stagedPath, Filename: fmt.Sprintf("fixture-%04d.deb", i), Package: "tparun-batch-fixture", Version: "1.0.0", Architecture: "all", Size: int64(len(data)), SHA256: digest, StagedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			}
			if err = saveStagingManifest(manifest); err != nil {
				t.Fatal(err)
			}

			const repositoryID = "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
			const sessionID = "abababababababababababababababab"
			totalBatches := (count + maxUploadFiles - 1) / maxUploadFiles
			var nextBatch, uploaded int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v1/repositories":
					_ = json.NewEncoder(w).Encode([]map[string]string{{"id": repositoryID, "name": "batch"}})
				case r.Method == http.MethodPost && r.URL.Path == "/v1/repositories/"+repositoryID+"/publication-sessions":
					var inputs []publicationPlanInput
					if err := json.NewDecoder(r.Body).Decode(&inputs); err != nil || len(inputs) != count {
						http.Error(w, "incomplete manifest", http.StatusBadRequest)
						return
					}
					_ = json.NewEncoder(w).Encode(publicationSessionStart{SessionID: sessionID, BatchSize: maxUploadFiles, Plan: publicationPlanResponse{RepositoryID: repositoryID, StagedPackageCount: int64(count), ProjectedPackageCount: int64(count), StorageLimitBytes: 1 << 30, Allowed: true}})
				case r.Method == http.MethodPost && r.URL.Path == "/v1/repositories/"+repositoryID+"/publishes":
					batch, err := strconv.Atoi(r.URL.Query().Get("batch"))
					if err != nil || batch != nextBatch || r.URL.Query().Get("session") != sessionID {
						http.Error(w, "batch order mismatch", http.StatusConflict)
						return
					}
					reader, err := r.MultipartReader()
					if err != nil {
						http.Error(w, "invalid multipart", http.StatusBadRequest)
						return
					}
					batchFiles := 0
					for {
						part, err := reader.NextPart()
						if err == io.EOF {
							break
						}
						if err != nil {
							http.Error(w, "invalid part", http.StatusBadRequest)
							return
						}
						if _, err = io.Copy(io.Discard, part); err != nil {
							http.Error(w, "invalid part body", http.StatusBadRequest)
							return
						}
						_ = part.Close()
						batchFiles++
					}
					want := maxUploadFiles
					if remaining := count - uploaded; remaining < want {
						want = remaining
					}
					if batchFiles != want || batchFiles > maxUploadFiles {
						http.Error(w, "incorrect batch file count", http.StatusBadRequest)
						return
					}
					uploaded += batchFiles
					nextBatch++
					_ = json.NewEncoder(w).Encode(map[string]any{"session_id": sessionID, "batch": batch, "state": "accepted"})
				case r.Method == http.MethodPost && r.URL.Path == "/v1/publication-sessions/"+sessionID+"/finalize":
					if uploaded != count || nextBatch != totalBatches {
						http.Error(w, "incomplete batches finalized", http.StatusConflict)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"id": sessionID, "state": "queued"})
				case r.Method == http.MethodGet && r.URL.Path == "/v1/publish-jobs/"+sessionID:
					_ = json.NewEncoder(w).Encode(map[string]string{"id": sessionID, "state": "succeeded"})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			oldStdout := os.Stdout
			readPipe, writePipe, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stdout = writePipe
			publishErr := publishStaged(cliConfig{Server: server.URL, Token: "token"}, repositoryID, true, false)
			os.Stdout = oldStdout
			_ = writePipe.Close()
			output, readErr := io.ReadAll(readPipe)
			_ = readPipe.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if publishErr != nil {
				t.Fatal(publishErr)
			}
			if uploaded != count || nextBatch != totalBatches {
				t.Fatalf("uploaded=%d batches=%d want files=%d batches=%d", uploaded, nextBatch, count, totalBatches)
			}
			for batch := 1; batch <= totalBatches; batch++ {
				if !strings.Contains(string(output), fmt.Sprintf("Uploading batch %d/%d ... accepted", batch, totalBatches)) {
					t.Fatalf("progress omitted batch %d/%d: %s", batch, totalBatches, output)
				}
			}
			if !strings.Contains(string(output), "Finalizing publication...") {
				t.Fatalf("progress omitted finalization: %s", output)
			}
		})
	}
}

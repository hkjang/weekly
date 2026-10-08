package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// An explicit PATCH can store the largest PostgreSQL integer. Reading the next
// order then overflows, but the failed upload must leave the report intact.
// guards: uploadAttachments
func TestAttachmentUploadOrderQueryFailurePreservesImagesAndAllowsRetry(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("capture_order_failure", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "순서 조회 실패 시 보존")
	path := fmt.Sprintf("/api/v1/reports/%d/attachments", reportID)
	ids := server.uploadCaptures(t, path, author, "a.png", "b.png")
	server.patchCapture(t, path, ids["a.png"], map[string]any{"sortOrder": 0}, author)
	server.patchCapture(t, path, ids["b.png"], map[string]any{"sortOrder": 2147483647}, author)
	before := server.listCaptures(t, path, author)
	filesBefore := server.attachmentFiles(t, reportID)
	if len(before) != 2 || len(filesBefore) != 2 {
		t.Fatalf("before refusal: rows=%d, files=%v, want two images", len(before), filesBefore)
	}
	if got := orderByFilename(before, placementAfter); !reflect.DeepEqual(got, map[string]int{"a.png": 0, "b.png": 2147483647}) {
		t.Fatalf("PATCH did not store the overflow fixture: %v", got)
	}
	assertOriginals := func() {
		t.Helper()
		for index, name := range []string{"a.png", "b.png"} {
			image := server.request(http.MethodGet, fmt.Sprintf("%s/%d", path, ids[name]), nil, author)
			if image.Code != http.StatusOK || !bytes.Equal(image.Body.Bytes(), samplePNG(t, 8+index, 8+index)) {
				t.Errorf("original %s GET=%d; want 200 and unchanged image bytes", name, image.Code)
			}
		}
	}
	assertOriginals()
	files := map[string][]byte{"c.png": samplePNG(t, 10, 10)}
	response := server.uploadMany(path, "files", files, author)
	if response.Code != http.StatusInternalServerError {
		t.Errorf("upload with an unreadable next order answered %d, want 500: %s", response.Code, response.Body.String())
	} else if message := refusal(t, response); !strings.HasPrefix(message, "QUERY_FAILED ") || !strings.Contains(message, "순서") {
		t.Errorf("order query failure must explain the failed order lookup: %s", message)
	}
	after := server.listCaptures(t, path, author)
	if !reflect.DeepEqual(after, before) {
		t.Errorf("refused upload changed attachment IDs, orders or rows: before=%v after=%v", before, after)
	}
	if got := server.attachmentFiles(t, reportID); !reflect.DeepEqual(got, filesBefore) {
		t.Errorf("refused upload changed disk files: before=%v after=%v", filesBefore, got)
	}
	assertOriginals()

	// Repair through the same public PATCH contract and retry the identical
	// image. It belongs after the existing maximum, exactly once.
	server.patchCapture(t, path, ids["b.png"], map[string]any{"sortOrder": 7}, author)
	retry := server.uploadMany(path, "files", files, author)
	if retry.Code != http.StatusCreated {
		t.Fatalf("retry after repairing order answered %d: %s", retry.Code, retry.Body.String())
	}
	var envelope struct {
		Success bool             `json:"success"`
		Data    []attachmentView `json:"data"`
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Success || len(envelope.Data) != 1 {
		t.Fatalf("retry must return one created image: %s", retry.Body.String())
	}
	created := envelope.Data[0]
	if created.ID == 0 || created.Filename != "c.png" || created.Placement != placementAfter || created.SortOrder != 8 || !created.Available {
		t.Errorf("retry returned an unexpected attachment: %+v", created)
	}
	listed := server.listCaptures(t, path, author)
	if len(listed) != 3 || !reflect.DeepEqual(orderByFilename(listed, placementAfter), map[string]int{"a.png": 0, "b.png": 7, "c.png": 8}) {
		t.Errorf("retry must store c.png once at the group end: %v", listed)
	}
	if len(listed) == 3 && listed[2].ID != created.ID {
		t.Errorf("listed last image ID=%d, want retry ID=%d", listed[2].ID, created.ID)
	}
	if got := server.attachmentFiles(t, reportID); len(got) != len(filesBefore)+1 {
		t.Errorf("retry should add one disk file: %v", got)
	}
	image := server.request(http.MethodGet, fmt.Sprintf("%s/%d", path, created.ID), nil, author)
	if image.Code != http.StatusOK || !bytes.Equal(image.Body.Bytes(), files["c.png"]) {
		t.Errorf("retried image GET=%d; want 200 and original image bytes", image.Code)
	}
	assertOriginals()
}

// Both placements count from their own maximum, including multi-image requests
// and gaps left by explicit sortOrder PATCHes.
// guards: uploadAttachments
func TestAttachmentUploadOrderCountsEachPlacementIndependently(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("capture_order_groups", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "삽입 위치별 첨부 순서")
	path := fmt.Sprintf("/api/v1/reports/%d/attachments", reportID)

	upload := func(placement string, start int, names ...string) []attachmentView {
		t.Helper()
		var buffer bytes.Buffer
		writer := multipart.NewWriter(&buffer)
		if err := writer.WriteField("placement", placement); err != nil {
			t.Fatal(err)
		}
		for index, name := range names {
			part, err := writer.CreateFormFile("files", name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write(samplePNG(t, 12+index, 12+index)); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, path, &buffer)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		request.Header.Set("Origin", "http://"+request.Host)
		request.AddCookie(author)
		response := httptest.NewRecorder()
		server.app.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("%s upload answered %d: %s", placement, response.Code, response.Body.String())
		}
		var envelope struct {
			Success bool             `json:"success"`
			Data    []attachmentView `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if !envelope.Success || len(envelope.Data) != len(names) {
			t.Fatalf("unexpected successful upload response: %s", response.Body.String())
		}
		for index, item := range envelope.Data {
			if item.ID == 0 || item.Filename != names[index] || item.Placement != placement || item.SortOrder != start+index || !item.Available || item.CreatedAt.IsZero() {
				t.Errorf("%s upload item=%+v, want %s at order %d", placement, item, names[index], start+index)
			}
		}
		return envelope.Data
	}

	after := upload(placementAfter, 0, "after-a.png", "after-b.png")
	server.patchCapture(t, path, after[1].ID, map[string]any{"sortOrder": 7}, author)
	upload(placementBefore, 0, "before-a.png")
	upload(placementBefore, 1, "before-b.png", "before-c.png")
	upload(placementAfter, 8, "after-c.png", "after-d.png")
	listed := server.listCaptures(t, path, author)
	if len(listed) != 7 {
		t.Fatalf("group uploads stored %d images, want 7", len(listed))
	}
	for placement, want := range map[string]map[string]int{
		placementBefore: {"before-a.png": 0, "before-b.png": 1, "before-c.png": 2},
		placementAfter:  {"after-a.png": 0, "after-b.png": 7, "after-c.png": 8, "after-d.png": 9},
	} {
		if got := orderByFilename(listed, placement); !reflect.DeepEqual(got, want) {
			t.Errorf("%s orders=%v, want %v", placement, got, want)
		}
	}
}

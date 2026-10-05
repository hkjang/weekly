package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// uploadCaptures posts images through the production upload route the panel
// uses and returns the stored id for each filename. The panel always sends
// placement=AFTER (AttachmentPanel.tsx), so this does too.
func (s *testServer) uploadCaptures(t *testing.T, path string, author *http.Cookie, names ...string) map[string]int64 {
	t.Helper()
	files := map[string][]byte{}
	for index, name := range names {
		files[name] = samplePNG(t, 8+index, 8+index)
	}
	response := s.uploadMany(path, "files", files, author)
	if response.Code != http.StatusCreated && response.Code != http.StatusOK {
		t.Fatalf("upload %v answered %d: %s", names, response.Code, response.Body.String())
	}
	var envelope struct {
		Data []attachmentView `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode the upload response: %v (%s)", err, response.Body.String())
	}
	if len(envelope.Data) != len(names) {
		t.Fatalf("upload %v created %d row(s)", names, len(envelope.Data))
	}
	ids := map[string]int64{}
	for _, item := range envelope.Data {
		ids[item.Filename] = item.ID
	}
	for _, name := range names {
		if ids[name] == 0 {
			t.Fatalf("the upload response has no id for %s: %s", name, response.Body.String())
		}
	}
	return ids
}

// patchCapture sends one PATCH with exactly the fields given, which is how the
// panel's placement select and its up/down buttons differ: the select sends
// placement alone, the buttons send sortOrder alone.
func (s *testServer) patchCapture(t *testing.T, path string, id int64, body map[string]any, author *http.Cookie) {
	t.Helper()
	response := s.request(http.MethodPatch, fmt.Sprintf("%s/%d", path, id), body, author)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH %v on capture %d answered %d: %s", body, id, response.Code, response.Body.String())
	}
}

// listCaptures reads the report's captures back through the list route.
func (s *testServer) listCaptures(t *testing.T, path string, author *http.Cookie) []attachmentView {
	t.Helper()
	response := s.request(http.MethodGet, path, nil, author)
	if response.Code != http.StatusOK {
		t.Fatalf("list captures answered %d: %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data []attachmentView `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode the capture list: %v (%s)", err, response.Body.String())
	}
	return envelope.Data
}

// orderByFilename reports the sortOrder each named capture holds in the given
// placement, so a failure can name the file rather than a row id.
func orderByFilename(items []attachmentView, placement string) map[string]int {
	result := map[string]int{}
	for _, item := range items {
		if item.Placement == placement {
			result[item.Filename] = item.SortOrder
		}
	}
	return result
}

// Moving a capture to the other side has to put it at the end of the side it
// arrives on.
//
// updateAttachment wrote placement on its own and left sort_order alone, while
// the upload route numbers a new image max(sort_order)+1 *within its placement*.
// So a number that leaves one group is handed out again inside it, and the
// capture carrying that number back collides with a row already sitting there.
// Two rows then share one sort_order, the list orders them by id instead of by
// what the writer chose, and the exported deck prints them in that order — and
// the panel's up/down buttons swap the two sort_orders, which with equal values
// is a no-op, so the writer cannot correct it either.
//
// Every step here is a request the panel actually makes: the upload always
// sends placement=AFTER, and the placement select sends placement alone.
//
// guards: updateAttachment
func TestMovingACaptureAcrossGivesItTheEndOfTheGroupItArrivesIn(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("capture_order", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "순서가 걸린 보고서")
	path := fmt.Sprintf("/api/v1/reports/%d/attachments", reportID)

	// 1. Two images: AFTER holds a=0, b=1.
	first := server.uploadCaptures(t, path, author, "a.png", "b.png")
	// 2. The select moves b to BEFORE, sending placement only.
	server.patchCapture(t, path, first["b.png"], map[string]any{"placement": placementBefore}, author)
	// 3. One more image. The upload counts inside AFTER alone, so b's old
	//    number is free again and c takes it.
	second := server.uploadCaptures(t, path, author, "c.png")
	// 4. The select moves c to BEFORE as well, where b is already sitting.
	server.patchCapture(t, path, second["c.png"], map[string]any{"placement": placementBefore}, author)

	items := server.listCaptures(t, path, author)
	if len(items) != 3 {
		t.Fatalf("the report holds %d captures, want 3", len(items))
	}
	for _, placement := range []string{placementBefore, placementAfter} {
		seen := map[int]string{}
		for _, item := range items {
			if item.Placement != placement {
				continue
			}
			if other, taken := seen[item.SortOrder]; taken {
				t.Errorf("%s holds %s and %s on the same sortOrder %d, so the deck orders them by id and the panel cannot swap them apart",
					placement, other, item.Filename, item.SortOrder)
			}
			seen[item.SortOrder] = item.Filename
		}
	}
	// And the capture that arrived last is at the end of the group it arrived in.
	before := orderByFilename(items, placementBefore)
	if before["c.png"] <= before["b.png"] {
		t.Errorf("BEFORE has b=%d and the newly moved c=%d; the arrival belongs after what was already there",
			before["b.png"], before["c.png"])
	}
}

// The panel's up/down buttons send sortOrder alone and must keep working
// exactly as they did: the swap writes the value it was given. And a request
// that carries both fields is the caller stating a position, so that value
// wins over the end of the destination group.
//
// guards: updateAttachment
func TestASentSortOrderIsStoredAsSent(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("capture_swap", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "맞바꾸기 보고서")
	path := fmt.Sprintf("/api/v1/reports/%d/attachments", reportID)

	ids := server.uploadCaptures(t, path, author, "a.png", "b.png")
	// The up button on b: two PATCHes, each carrying sortOrder alone.
	server.patchCapture(t, path, ids["b.png"], map[string]any{"sortOrder": 0}, author)
	server.patchCapture(t, path, ids["a.png"], map[string]any{"sortOrder": 1}, author)
	after := orderByFilename(server.listCaptures(t, path, author), placementAfter)
	if after["a.png"] != 1 || after["b.png"] != 0 {
		t.Errorf("after the swap AFTER holds a=%d, b=%d, want a=1, b=0", after["a.png"], after["b.png"])
	}

	// placement and sortOrder together: the sent position wins, rather than
	// being overwritten with the end of the destination group.
	server.patchCapture(t, path, ids["a.png"], map[string]any{"placement": placementBefore, "sortOrder": 7}, author)
	listed := server.listCaptures(t, path, author)
	moved := orderByFilename(listed, placementBefore)
	if moved["a.png"] != 7 {
		t.Errorf("a moved with sortOrder=7 landed on %d; a stated position has to be kept", moved["a.png"])
	}
}

// Re-sending the placement a capture already has must not renumber it. The
// panel's select fires on every change event, and a writer who picks the side
// the image is already on would otherwise see it jump to the end of its own
// group for no reason.
//
// guards: updateAttachment
func TestResendingTheSamePlacementLeavesTheOrderAlone(t *testing.T) {
	server := newTestServer(t)
	author := server.createUser("capture_resend", "USER", nil)
	reportID, _ := server.draft(author, "2026-08-24", "같은 위치 보고서")
	path := fmt.Sprintf("/api/v1/reports/%d/attachments", reportID)

	ids := server.uploadCaptures(t, path, author, "a.png", "b.png")
	// a is first in AFTER; sending AFTER again is not a move.
	server.patchCapture(t, path, ids["a.png"], map[string]any{"placement": placementAfter}, author)
	// A lower-case value is the same placement too — the handler upper-cases it.
	server.patchCapture(t, path, ids["a.png"], map[string]any{"placement": "after"}, author)
	// And a caption-only PATCH touches nothing about the order.
	server.patchCapture(t, path, ids["a.png"], map[string]any{"caption": "배포 화면"}, author)

	after := orderByFilename(server.listCaptures(t, path, author), placementAfter)
	if after["a.png"] != 0 || after["b.png"] != 1 {
		t.Errorf("AFTER holds a=%d, b=%d after re-sending the placement it already had, want a=0, b=1",
			after["a.png"], after["b.png"])
	}
}

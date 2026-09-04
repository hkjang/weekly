package app

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/mail"
	"strings"
	"testing"
	"time"
)

// The mail is half of what this feature is for. A weekly report goes into a
// meeting as a deck, and for as long as this existed the mail carried the text
// and nothing else — every reader who wanted the file had to come back to the
// screen and download it, which is the trip the mail was supposed to save.

// attachedFiles returns the parts of one message off the wire: the text a
// reader sees, and every file hanging off it.
//
// Parsed with net/mail and mime/multipart rather than by looking for substrings,
// because "the bytes are in there somewhere" is not the claim. The claim is that
// a mail client can find the file, which is a claim about structure.
func attachedFiles(t *testing.T, raw string) (string, []mailAttachment) {
	t.Helper()
	message, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("the message does not parse as a mail: %v\n%s", err, raw)
	}
	mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("Content-Type does not parse: %v", err)
	}
	if mediaType != "multipart/mixed" {
		t.Fatalf("the message is %s, so it carries no files at all", mediaType)
	}
	var text string
	var files []mailAttachment
	reader := multipart.NewReader(message.Body, params["boundary"])
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read a part: %v", err)
		}
		// NextPart decodes quoted-printable on its own but never base64, which
		// is what every part here is.
		encoded, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read part content: %v", err)
		}
		content := decodeBase64Lines(t, string(encoded))
		partType, partParams, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("a part's Content-Type does not parse: %v", err)
		}
		disposition, dispositionParams, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if disposition != "attachment" {
			text = string(content)
			continue
		}
		name := dispositionParams["filename"]
		if name == "" {
			name = partParams["name"]
		}
		files = append(files, mailAttachment{Filename: name, ContentType: partType, Content: content})
	}
	return text, files
}

func decodeBase64Lines(t *testing.T, encoded string) []byte {
	t.Helper()
	content, err := base64.StdEncoding.DecodeString(
		strings.NewReplacer("\r", "", "\n", "").Replace(encoded))
	if err != nil {
		t.Fatalf("a part is not the base64 its header claims: %v", err)
	}
	return content
}

// guards: buildMailMessage, wrapBase64, mailFileParameter
func TestAReportMailCarriesTheDeckAsAFileAClientCanOpen(t *testing.T) {
	deck := []byte("PK\x03\x04 이것은 발표자료입니다")
	raw := string(buildMailMessage("weekly@internal.test", "주간보고", "reader@internal.test",
		"2026-08-24 주간보고", "본문입니다",
		mailAttachment{Filename: "20260824_사용자 3_주간업무보고.pptx", ContentType: pptxMediaType, Content: deck}))

	// Everything outside a part is still ASCII: a multipart message is exactly
	// as exposed to a relay that is not 8-bit clean as a single-part one was.
	head, _, _ := strings.Cut(raw, "\r\n\r\n")
	for _, line := range strings.Split(head, "\r\n") {
		for _, r := range line {
			if r > 127 {
				t.Fatalf("a header carries a non-ASCII rune %q: %s", r, line)
			}
		}
	}

	text, files := attachedFiles(t, raw)
	if text != "본문입니다" {
		t.Errorf("the text of the mail is %q", text)
	}
	if len(files) != 1 {
		t.Fatalf("the mail carries %d files, want 1", len(files))
	}
	if files[0].Filename != "20260824_사용자 3_주간업무보고.pptx" {
		t.Errorf("the file arrived named %q", files[0].Filename)
	}
	if files[0].ContentType != pptxMediaType {
		t.Errorf("the file is typed %q, so a client would not open it in PowerPoint", files[0].ContentType)
	}
	if !bytes.Equal(files[0].Content, deck) {
		t.Errorf("the file that arrived is not the file that was sent: %q", files[0].Content)
	}
	// The plain parameter beside it is all an older client reads, and three
	// weeks saved into one folder have to be three files.
	if !strings.Contains(raw, `filename="20260824_`+mailAttachmentFallbackName+`"`) {
		t.Errorf("the fallback name does not say which week this is:\n%s", raw[:600])
	}
}

// A mail with nothing to attach must stay exactly the message it always was.
// A paperclip on every mail, most of them empty, is a paperclip nobody reads.

// guards: buildMailMessage
func TestAMailWithNothingToAttachStaysASinglePart(t *testing.T) {
	raw := string(buildMailMessage("weekly@internal.test", "주간보고", "reader@internal.test", "제목", "본문"))
	head, _, _ := strings.Cut(raw, "\r\n\r\n")
	if !strings.Contains(head, "Content-Type: text/plain; charset=UTF-8") {
		t.Errorf("a mail with no attachment is no longer plain text:\n%s", head)
	}
	if strings.Contains(head, "multipart") || strings.Contains(head, "boundary") {
		t.Errorf("a mail with no attachment was wrapped in multipart anyway:\n%s", head)
	}
}

// The names are Korean and can contain a semicolon, which ends a parameter. A
// name that ends the parameter early is a file a client either refuses or saves
// under half a name.

// guards: rfc2231Value, asciiMailFilename
func TestAnAwkwardFileNameSurvivesBothKindsOfClient(t *testing.T) {
	raw := string(buildMailMessage("weekly@internal.test", "", "reader@internal.test", "제목", "본문",
		mailAttachment{Filename: `1분기; 계획 "확정".pptx`, ContentType: pptxMediaType, Content: []byte("deck")}))
	_, files := attachedFiles(t, raw)
	if len(files) != 1 {
		t.Fatalf("the mail carries %d files, want 1", len(files))
	}
	if files[0].Filename != `1분기; 계획 "확정".pptx` {
		t.Errorf("a client reading RFC 2231 sees %q", files[0].Filename)
	}
	// And the plain parameter beside it, which is all an older client reads, is
	// still a name that parses and still ends in .pptx.
	if !strings.Contains(raw, `filename="`+mailAttachmentFallbackName+`"`) {
		t.Errorf("there is no plain filename for a client that cannot read the encoded one:\n%s", raw)
	}
}

// guards: reportMailAttachments, reportDeck
func TestTheMailedDeckIsTheSameFileTheDownloadGives(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	server.configureRelay(relay)
	writer := server.createUser("mail_deck", "USER", nil)

	if w := server.request(http.MethodPut, "/api/v1/me/mail",
		map[string]any{"address": "deck@internal.test", "onSubmit": true}, writer); w.Code != http.StatusOK {
		t.Fatalf("save the preference: %d %s", w.Code, w.Body.String())
	}
	id := server.submitted(writer, "2026-08-24", "첨부까지 확인합니다")

	messages := relay.awaitRelay(t, 1)
	if len(messages) != 1 {
		t.Fatalf("the relay received %d messages, want 1", len(messages))
	}
	text, files := attachedFiles(t, messages[0])
	if !strings.Contains(text, "2026-08-24 주간보고") {
		t.Errorf("the text of the mail is not this week's report:\n%s", text)
	}
	if len(files) != 1 {
		t.Fatalf("the submitted report was mailed with %d files, want 1", len(files))
	}

	// A PPTX is a zip, and a zip that does not open is a file a reader clicks
	// on and gets an error from.
	archive, err := zip.NewReader(bytes.NewReader(files[0].Content), int64(len(files[0].Content)))
	if err != nil {
		t.Fatalf("the attached deck does not open as a PPTX: %v", err)
	}
	slides := 0
	for _, file := range archive.File {
		if isSlideXML(file.Name) {
			slides++
		}
	}
	if slides == 0 {
		t.Errorf("the attached deck has no slides in it")
	}

	// And it is the same file, not a second rendering that happens to look
	// similar: two exports of one unchanged report are byte-identical, so the
	// mailed one has to match the downloaded one exactly.
	download := server.request(http.MethodGet, fmt.Sprintf("/api/v1/reports/%d/export.pptx", id), nil, writer)
	if download.Code != http.StatusOK {
		t.Fatalf("download the deck: %d %s", download.Code, download.Body.String())
	}
	if sha256.Sum256(files[0].Content) != sha256.Sum256(download.Body.Bytes()) {
		t.Errorf("the mailed deck (%d bytes) is not the file the download gives (%d bytes)",
			len(files[0].Content), download.Body.Len())
	}
}

// Everything above happens on a Monday, a week from now, once. A writer setting
// this up has no way to find out today whether the address they typed receives —
// and "I received nothing" reads the same whether the address was wrong, the
// relay was down, or the administrator never configured one.

// guards: testMyReportMail, mailRecipientFor
func TestAWriterCanSendThemselvesTheRealMailBeforeMonday(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	server.configureRelay(relay)
	writer := server.createUser("mail_selftest", "USER", nil)

	// Saved, but not turned on: the test must not be a submission in disguise,
	// and nothing may be queued by it.
	if w := server.request(http.MethodPut, "/api/v1/me/mail",
		map[string]any{"address": "selftest@internal.test", "onSubmit": false}, writer); w.Code != http.StatusOK {
		t.Fatalf("save the preference: %d %s", w.Code, w.Body.String())
	}
	server.submitted(writer, "2026-08-24", "시험 발송으로 확인합니다")

	sent := server.request(http.MethodPost, "/api/v1/me/mail/test", nil, writer)
	if sent.Code != http.StatusOK {
		t.Fatalf("send the test: %d %s", sent.Code, sent.Body.String())
	}
	reply := decodeData(t, sent)
	if reply["to"] != "selftest@internal.test" || reply["weekStart"] != "2026-08-24" {
		t.Errorf("the reply says it went to %v for week %v", reply["to"], reply["weekStart"])
	}
	if attached, _ := reply["attachment"].(string); !strings.HasSuffix(attached, ".pptx") {
		t.Errorf("the reply does not name an attached deck: %q", attached)
	}

	messages := relay.awaitRelay(t, 1)
	if len(messages) != 1 {
		t.Fatalf("the relay received %d messages, want 1", len(messages))
	}
	text, files := attachedFiles(t, messages[0])
	if !strings.Contains(text, "2026-08-24 주간보고") {
		t.Errorf("the test mail is not the writer's own report:\n%s", text)
	}
	if len(files) != 1 {
		t.Errorf("the test mail carried %d files, want 1 — the point is to prove the deck arrives", len(files))
	}
	if envelope := strings.Join(relay.envelopeLines(), " "); !strings.Contains(envelope, "selftest@internal.test") {
		t.Errorf("the test went somewhere other than the saved address: %s", envelope)
	}

	// It is a test, not a delivery: nothing may appear in the writer's history
	// as a week that was sent.
	var recorded int
	if err := server.app.db.QueryRow(server.ctx(),
		`SELECT count(*) FROM report_mail_deliveries`).Scan(&recorded); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if recorded != 0 {
		t.Errorf("%d deliveries were recorded for a test send", recorded)
	}

	// And the button cannot be held down: every press renders a deck and then
	// holds a request open for the whole relay timeout.
	again := server.request(http.MethodPost, "/api/v1/me/mail/test", nil, writer)
	if again.Code != http.StatusTooManyRequests {
		t.Errorf("a second test one second later answered %d, want 429", again.Code)
	}
}

// guards: mailRecipientFor
func TestATestMailWithNowhereToGoSaysSoInsteadOfSending(t *testing.T) {
	server := newTestServer(t)
	relay := startFakeRelay(t)
	server.configureRelay(relay)
	silent := server.createUser("mail_noaddress", "USER", nil)

	refused := server.request(http.MethodPost, "/api/v1/me/mail/test", nil, silent)
	if refused.Code != http.StatusBadRequest {
		t.Fatalf("a writer with no address got %d, want 400: %s", refused.Code, refused.Body.String())
	}
	if !strings.Contains(refused.Body.String(), "MAIL_ADDRESS_REQUIRED") {
		t.Errorf("the refusal does not name what is missing: %s", refused.Body.String())
	}
	if messages := relay.received(); len(messages) != 0 {
		t.Errorf("the relay received %d messages for a writer with no address", len(messages))
	}
}

// The cooldown is one writer's turn, not a queue for everybody. A shared
// window would make one person's test block three hundred others' on a Monday
// morning, which is exactly when they are all setting this up.

// guards: take, newSendCooldown
func TestOneWritersTestDoesNotHoldUpAnothers(t *testing.T) {
	cooldown := newSendCooldown()
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	window := 30 * time.Second
	if _, ok := cooldown.take(1, window, now); !ok {
		t.Fatalf("the first test was refused")
	}
	if _, ok := cooldown.take(2, window, now); !ok {
		t.Errorf("somebody else's test was refused because a different person had just run one")
	}
	remaining, ok := cooldown.take(1, window, now.Add(5*time.Second))
	if ok {
		t.Errorf("the same person ran a second test five seconds later")
	}
	if remaining <= 0 || remaining > 25*time.Second {
		t.Errorf("the refusal says %v is left of a 30s window that started 5s ago", remaining)
	}
	if _, ok := cooldown.take(1, window, now.Add(31*time.Second)); !ok {
		t.Errorf("the window never ended")
	}
}

// A relay that cannot be used has to be said, not attempted: the message an
// SMTP dial failure produces names this deployment's network, not what the
// writer should do about it.

// guards: mailSettingsForTest
func TestATestMailWithNoRelayConfiguredNamesTheSetting(t *testing.T) {
	server := newTestServer(t)
	writer := server.createUser("mail_norelay", "USER", nil)
	if w := server.request(http.MethodPut, "/api/v1/me/mail",
		map[string]any{"address": "norelay@internal.test", "onSubmit": false}, writer); w.Code != http.StatusOK {
		t.Fatalf("save the preference: %d %s", w.Code, w.Body.String())
	}
	refused := server.request(http.MethodPost, "/api/v1/me/mail/test", nil, writer)
	if refused.Code != http.StatusBadRequest {
		t.Fatalf("with no relay configured the test answered %d, want 400: %s", refused.Code, refused.Body.String())
	}
	if !strings.Contains(refused.Body.String(), "메일 발송이 꺼져 있습니다") {
		t.Errorf("the refusal does not say which setting is off: %s", refused.Body.String())
	}
}

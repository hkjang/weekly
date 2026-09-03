package app

import (
	"net/mail"
	"strings"
	"testing"
)

// The sender name is a settings box an administrator types into, and everything
// this product had been configured with was Korean — which the encoded-word
// wraps in base64, so whatever was typed came out as text. An English name is
// not encoded, by design, and reaches the From header as itself. RFC 5322 reads
// a comma there as the end of one address and the start of another, and angle
// brackets as an address of their own, so "Weekly, Inc." sent every report from
// a sender named Weekly and "Weekly <quiet@elsewhere.test>" sent it from an
// address nobody at this company owns.

// guards: mailDisplayName, buildMailMessage
func TestTheSenderNameCannotBecomeAnAddress(t *testing.T) {
	const address = "weekly@internal.test"
	names := []string{
		"주간보고",                          // what the product ships with: an encoded-word, unchanged.
		"Weekly",                        // plain enough to write plainly.
		"Weekly, Inc.",                  // the comma that used to end the address.
		"Weekly <quiet@elsewhere.test>", // an address the name supplied for itself.
		"Weekly (주간보고) 알림",              // parentheses are a comment, not a name.
		`He said "weekly"`,              // quotes have to survive being quoted.
		`back\slash`,                    // and so does the escape character.
		"주간보고, 알림",                      // special and non-ASCII at once.
	}
	for _, name := range names {
		raw := string(buildMailMessage(address, name, "reader@internal.test", "제목", "본문"))
		parsed, err := mail.ReadMessage(strings.NewReader(raw))
		if err != nil {
			t.Errorf("보내는 이름 %q 이 든 메시지를 파서가 읽지 못합니다: %v", name, err)
			continue
		}
		senders, err := parsed.Header.AddressList("From")
		if err != nil {
			t.Errorf("보내는 이름 %q: From 을 주소로 읽을 수 없습니다: %v (%q)",
				name, err, parsed.Header.Get("From"))
			continue
		}
		// One sender, not two: the failure this guards for is a name that adds
		// its own entry to the list, which a check on the first entry alone
		// would not see.
		if len(senders) != 1 {
			t.Errorf("보내는 이름 %q 이 보낸사람 %d 명이 되었습니다: %q",
				name, len(senders), parsed.Header.Get("From"))
			continue
		}
		if senders[0].Address != address {
			t.Errorf("보내는 이름 %q 이 보내는 주소를 %q 로 바꿨습니다: %q",
				name, senders[0].Address, parsed.Header.Get("From"))
		}
		if senders[0].Name != name {
			t.Errorf("보내는 이름이 %q 에서 %q 로 바뀌어 도착합니다: %q",
				name, senders[0].Name, parsed.Header.Get("From"))
		}
	}
}

// mailDisplayName is deliberately not a subject here: the point of this guard is
// that a blank name never reaches it.
//
// guards: buildMailMessage
func TestASenderNameOfNothingLeavesJustTheAddress(t *testing.T) {
	const address = "weekly@internal.test"
	// A settings box that was cleared, and one that has a space left in it, are
	// the same intention. The second used to write a display name made of
	// whitespace in front of the address — legible to a parser, but it is a name
	// nobody asked for, and it puts a name where the encoded-word case would put
	// one, so the two forms of "no name" arrive looking different.
	for _, name := range []string{"", "   ", "\r\n"} {
		raw := string(buildMailMessage(address, name, "reader@internal.test", "제목", "본문"))
		parsed, err := mail.ReadMessage(strings.NewReader(raw))
		if err != nil {
			t.Errorf("보내는 이름 %q 이 든 메시지를 파서가 읽지 못합니다: %v", name, err)
			continue
		}
		if got := parsed.Header.Get("From"); got != address {
			t.Errorf("이름 없는 보내는 사람이 %q 로 쓰였습니다, 원한 것은 %q", got, address)
		}
	}
}

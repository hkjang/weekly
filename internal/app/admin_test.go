package app

import (
	"encoding/json"
	"strings"
	"testing"
)

// A directory the caller can page through has to be searchable, or the account
// on page three is simply gone: this screen had no filter at all and rendered
// every row, so the only way to find somebody was the browser's own
// find-in-page over whatever had loaded.
//
// The reviewer picker is the reason the role filter exists. Built from a page
// of the table it would quietly stop offering reviewers as the directory grew,
// and nobody would see that a name was missing rather than ineligible.
func TestAdminUserPageCarriesWhatItLeftOut(t *testing.T) {
	page := userListView{
		Items:  make([]userView, userPageDefault),
		Total:  301,
		Limit:  userPageDefault,
		Offset: 0,
		Query:  "u10",
		Roles:  []string{"TEAM_LEADER"},
	}
	body, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"total":301`, `"limit":100`, `"offset":0`, `"query":"u10"`, `"roles":["TEAM_LEADER"]`} {
		if !strings.Contains(string(body), key) {
			t.Errorf("the response does not carry %s", key)
		}
	}
	// Absent rather than empty when unused: a reader should not have to work
	// out whether roles:[] means "no filter" or "no role matched".
	plain, err := json.Marshal(userListView{Items: []userView{}, Total: 0, Limit: userPageDefault})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), `"query"`) || strings.Contains(string(plain), `"roles"`) {
		t.Errorf("an unfiltered page still advertises a filter: %s", string(plain))
	}
}

package app

import "testing"

func TestConfluenceBodyMatchesSearchVersion(t *testing.T) {
	activity := confluenceActivity{Page: ConfluencePage{ID: "123", Version: 7}}
	tests := []struct {
		name string
		body *ConfluencePageBody
		want bool
	}{
		{name: "same version", body: &ConfluencePageBody{PageID: "123", Version: 7}, want: true},
		{name: "changed version", body: &ConfluencePageBody{PageID: "123", Version: 8}, want: false},
		{name: "wrong page", body: &ConfluencePageBody{PageID: "999", Version: 7}, want: false},
		{name: "legacy missing version", body: &ConfluencePageBody{PageID: "123"}, want: true},
		{name: "nil body", body: nil, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := confluenceBodyMatchesActivity(test.body, activity); got != test.want {
				t.Fatalf("match = %v, want %v", got, test.want)
			}
		})
	}
}

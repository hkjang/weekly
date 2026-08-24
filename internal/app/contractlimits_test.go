package app

import (
	"fmt"
	"net/http"
	"testing"
)

// docs/openapi.yaml makes the same promise for every paged list: a default, a
// maximum, and "범위를 벗어나면 잘라 맞추고, 읽을 수 없는 값은 기본값으로 본다".
// A promise made once per endpoint in prose is a promise nobody re-reads, and
// an endpoint that answers 4,000 rows to limit=99999 breaks a client's memory,
// not its own.
//
// guards: clampQueryInt=100
func TestPagedListsHonourTheLimitsTheContractPublishes(t *testing.T) {
	server := newTestServer(t)

	// path, documented default, documented maximum
	endpoints := []struct {
		path          string
		fallback, cap int
	}{
		{"/api/v1/reports", 200, 500},
		{"/api/v1/team/reports", 200, 500},
		{"/api/v1/import/history", 50, 200},
		{"/api/v1/admin/audit", 50, 200},
	}
	for _, endpoint := range endpoints {
		cases := []struct {
			name, query string
			want        int
		}{
			{"생략하면 기본값", "", endpoint.fallback},
			{"상한을 넘으면 상한", fmt.Sprintf("?limit=%d", endpoint.cap*100), endpoint.cap},
			// The contract clamps a readable value to the nearer end, so 0 and
			// -5 are 1 rather than the default. Only an unreadable value falls
			// back — a distinction easy to get backwards, which is why the
			// sentence in docs/openapi.yaml now spells it out.
			{"0이면 하한", "?limit=0", 1},
			{"음수면 하한", "?limit=-5", 1},
			{"숫자가 아니면 기본값", "?limit=열개만", endpoint.fallback},
			{"상한 자체는 그대로", fmt.Sprintf("?limit=%d", endpoint.cap), endpoint.cap},
		}
		for _, item := range cases {
			w := server.request(http.MethodGet, endpoint.path+item.query, nil, server.admin)
			if w.Code != http.StatusOK {
				t.Errorf("%s%s: %d %s", endpoint.path, item.query, w.Code, w.Body.String())
				continue
			}
			data := decodeData(t, w)
			limit, ok := data["limit"].(float64)
			if !ok {
				t.Errorf("%s%s: the response carries no limit, so a client cannot tell a page from the whole set: %s",
					endpoint.path, item.query, w.Body.String())
				continue
			}
			if int(limit) != item.want {
				t.Errorf("%s%s (%s): limit=%d, the contract says %d",
					endpoint.path, item.query, item.name, int(limit), item.want)
			}
			// The other half of the same rule: a page has to say how many exist.
			if _, present := data["total"]; !present {
				t.Errorf("%s%s: no total, so this page reads as the whole set", endpoint.path, item.query)
			}
		}
		// A negative offset is a client bug, and PostgreSQL answers it with an
		// error. The list has to absorb it rather than turn it into a 500.
		for _, bad := range []string{"?offset=-1", "?offset=열개만"} {
			w := server.request(http.MethodGet, endpoint.path+bad, nil, server.admin)
			if w.Code != http.StatusOK {
				t.Errorf("%s%s: %d %s", endpoint.path, bad, w.Code, w.Body.String())
			}
		}
	}
}

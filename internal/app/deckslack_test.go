package app

import (
	"fmt"
	"testing"
)

// A deck's first page is the one everybody sees. Slack belongs at the end.
//
// distributeReferenceItems minimises the tallest slide and breaks ties on a
// squared load so short items spread evenly. Both scores tie constantly — a
// week of similarly sized tasks ties every time — and the tie went to the first
// candidate, which is the smallest break, which puts the slack at the front.
// Measured on a real export before this test existed: seven tasks over four
// pages came out 1·2·2·2, so the deck opened on a page holding one task; two
// pages came out 3·4, 4·5 and 5·6, always the thinner page first.
//
// This asserts the shape, not the tie rule, so a future cost model is free to
// reach it any way it likes: no page may hold fewer items than a page after it
// when every item costs the same.
func TestADeckOfEqualTasksPutsItsSlackAtTheEnd(t *testing.T) {
	for _, count := range []int{2, 3, 5, 7, 9, 11, 12} {
		items := make([]reportItem, count)
		for index := range items {
			items[index] = reportItem{
				Category:      "운영",
				Title:         fmt.Sprintf("업무 %d", index+1),
				CurrentResult: "같은 길이의 실적입니다",
				NextPlan:      "같은 길이의 계획입니다",
			}
		}
		for _, slides := range []int{2, 3, 4} {
			groups := distributeReferenceItems(items, slides)
			shape := make([]int, 0, len(groups))
			for _, group := range groups {
				shape = append(shape, len(group))
			}
			// Trailing empties are how the renderer learns to emit fewer pages
			// than the template holds; they are not slack on a rendered page.
			filled := shape
			for len(filled) > 0 && filled[len(filled)-1] == 0 {
				filled = filled[:len(filled)-1]
			}
			total := 0
			for index := 1; index < len(filled); index++ {
				if filled[index] > filled[index-1] {
					t.Errorf("%d개 업무를 %d장에 나누니 %v — %d번째 장이 앞 장보다 많습니다",
						count, slides, shape, index+1)
				}
			}
			for _, size := range filled {
				total += size
			}
			if total != count {
				t.Errorf("%d개 업무를 %d장에 나누니 %v — 합이 %d입니다", count, slides, shape, total)
			}
		}
	}
}

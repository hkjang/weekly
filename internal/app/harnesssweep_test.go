package app

import (
	"fmt"
	"testing"
	"time"
)

// The sweep decides what to delete from a name alone, so the name reader is the
// only thing standing between an abandoned database and a working one. Reading
// a live run's database as stale would delete the template a suite running
// beside this one is about to copy.

// These two carry no `guards:` marker on purpose. The marker names a function
// for guard-check.py to measure with a coverage profile, and Go does not report
// coverage for code in _test.go files — so naming harnessNameStamp there makes
// the check report a guard that "never executed" whatever the test does. The
// convention is for the product's functions; this is the harness checking
// itself.
func TestTheSweepReadsAgeFromTheNameAndNothingElse(t *testing.T) {
	now := time.Now().Unix()

	for _, live := range []string{
		fmt.Sprintf("weekly_tmpl_%d_314159", now),
		fmt.Sprintf("weekly_h_%d_314159_7", now),
	} {
		stamp, ok := harnessNameStamp(live)
		if !ok {
			t.Errorf("%s: 이름에서 시각을 읽지 못했습니다", live)
			continue
		}
		if stamp != now {
			t.Errorf("%s: 시각을 %d 로 읽었습니다, %d 이어야 합니다", live, stamp, now)
		}
	}

	// The old naming carries a run counter where the stamp now goes. Those
	// names cannot be dated, and nothing running today makes one.
	for _, legacy := range []string{"weekly_tmpl_82606504", "weekly_h_798832559_1"} {
		if _, ok := harnessNameStamp(legacy); ok {
			t.Errorf("%s: 옛 이름에서 시각을 읽었다고 답합니다", legacy)
		}
	}

	// And nothing else in the database list is ours to touch.
	for _, other := range []string{"weekly", "weeklyscale", "weeklytpl", "postgres", "weekly_backup_src"} {
		if _, ok := harnessNameStamp(other); ok {
			t.Errorf("%s: 하네스가 만든 것이 아닌데 자기 것이라고 답합니다", other)
		}
	}
}

func TestADatabaseFromThisRunIsNeverStale(t *testing.T) {
	// The run that is executing this test must not be able to delete its own
	// working template, however long the suite takes.
	stamp, ok := harnessNameStamp(fmt.Sprintf("weekly_tmpl_%d_%d", harnessStamp, harnessRun))
	if !ok {
		t.Fatal("이번 실행이 만든 이름을 읽지 못합니다")
	}
	if cutoff := time.Now().Add(-harnessStale).Unix(); stamp < cutoff {
		t.Errorf("이번 실행의 데이터베이스가 이미 오래된 것으로 판정됩니다: %d < %d", stamp, cutoff)
	}
}

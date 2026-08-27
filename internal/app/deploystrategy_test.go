package app

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The shipped Kubernetes manifest asks for one process. The default rollout
// strategy runs two.
//
// Reproduced with two containers on one database: the newcomer's startup
// recovery — the statement that revives work a crash left behind — reset an
// import that was not abandoned but held by the process still running, then
// picked it up itself. Both analysed the same PPTX at once: two connections
// open to the AI gateway, two writes to one import_files row, and started_at
// rewritten to the clock of the process that was not doing the work. The
// Confluence sync carries the same shape of startup recovery.
//
// ReadWriteOnce points the same way: a new pod scheduled to another node
// cannot attach until the old one lets go, and the rollout stops there.
//
// No `guards:` marker. This does not exercise a function; it pairs a shipped
// file with the rule that has to hold in it, the way the contract and README
// pairings do.
func TestTheDeploymentReplacesItsPodRatherThanOverlapping(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/kubernetes.yaml")
	if err != nil {
		t.Skipf("매니페스트를 여기서 읽을 수 없습니다: %v", err)
	}
	manifest := string(raw)

	// Whatever the manifest asks for has to be one process at a time.
	replicas := regexp.MustCompile(`(?m)^\s*replicas:\s*(\d+)`).FindStringSubmatch(manifest)
	if replicas == nil {
		t.Fatal("replicas 를 찾지 못했습니다")
	}
	if replicas[1] != "1" {
		t.Errorf("replicas 가 %s 입니다. 배경 작업자들이 한 프로세스를 전제합니다", replicas[1])
	}
	if !regexp.MustCompile(`(?m)^\s*strategy:\s*\n\s*type:\s*Recreate\b`).MatchString(manifest) {
		t.Error("strategy: Recreate 가 없습니다. 기본값 RollingUpdate 는 업그레이드 동안 두 프로세스를 겹쳐 돌립니다")
	}
	// The volume that makes overlap impossible on a multi-node cluster, and the
	// reason the rollout would hang rather than merely misbehave.
	if !strings.Contains(manifest, "ReadWriteOnce") {
		t.Error("ReadWriteOnce 클레임이 사라졌습니다. 이 시험의 전제가 바뀐 것입니다")
	}
	// Compose recreates the container in place, so the same rule holds there
	// without a setting; what must not appear is a scaled-up service.
	composeRaw, err := os.ReadFile("../../deploy/compose.yaml")
	if err == nil {
		if regexp.MustCompile(`(?m)^\s*replicas:\s*([2-9]|\d\d)`).MatchString(string(composeRaw)) {
			t.Error("compose 가 두 벌 이상을 띄웁니다")
		}
	}
}

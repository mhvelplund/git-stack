package gitlab

import (
	"errors"
	"reflect"
	"testing"

	"github.com/mhvelplund/git-stack/pkg/protocol"
)

func TestStripDraft(t *testing.T) {
	for in, want := range map[string]string{
		"Draft: Add x":        "Add x",
		"draft:[Draft] Add x": "Add x",
		"(Draft) Add x":       "Add x",
		"Add draft: mode":     "Add draft: mode",
	} {
		if got := StripDraft(in); got != want {
			t.Errorf("StripDraft(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAPIArgsAndErrors(t *testing.T) {
	var got []string
	c := &Client{Project: Project{"gl.example", "g/r"}, Run: func(args ...string) ([]byte, error) {
		got = args
		return []byte(`[{"iid":4,"state":"merged"},{"iid":5,"state":"opened"}]`), nil
	}}
	mr, err := c.FindMRForBranch("feat")
	if err != nil || mr.IID != 5 {
		t.Fatalf("FindMRForBranch = %+v, %v", mr, err)
	}
	want := []string{"api", "--hostname", "gl.example", "-X", "GET", "projects/g%2Fr/merge_requests",
		"-f", "order_by=created_at", "-f", "sort=desc", "-f", "source_branch=feat", "-f", "state=all"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args =\n%v\nwant\n%v", got, want)
	}

	c.Run = func(...string) ([]byte, error) { return nil, errors.New("401 Unauthorized") }
	if _, err := c.GetMR(1); protocol.CodeOf(err) != protocol.CodeAuth {
		t.Fatalf("GetMR error code = %s", protocol.CodeOf(err))
	}
}

func TestForkMRsAreIgnored(t *testing.T) {
	c := &Client{Project: Project{"gl.example", "g/r"}, Run: func(args ...string) ([]byte, error) {
		return []byte(`[{"iid":9,"state":"opened","source_project_id":77,"target_project_id":5},{"iid":4,"state":"merged","source_project_id":5,"target_project_id":5}]`), nil
	}}
	if mr, err := c.FindMRForBranch("fix"); err != nil || mr == nil || mr.IID != 4 {
		t.Fatalf("FindMRForBranch = %+v, %v", mr, err)
	}
	if list, err := c.ListMRsTargeting("fix"); err != nil || len(list) != 1 || list[0].IID != 4 {
		t.Fatalf("ListMRsTargeting = %+v, %v", list, err)
	}
}

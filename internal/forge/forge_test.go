package forge

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mhvelplund/git-stack/pkg/protocol"
)

func TestParseRemoteURL(t *testing.T) {
	cases := map[string][2]string{
		"git@gitlab.com:group/repo.git":                     {"gitlab.com", "group/repo"},
		"ssh://git@gitlab.example:2222/a/b/c.git":           {"gitlab.example", "a/b/c"},
		"https://gitlab.com/group/sub/repo":                 {"gitlab.com", "group/sub/repo"},
		"https://oauth2:tok@gitlab.com/group/repo.git":      {"gitlab.com", "group/repo"},
		"work-gl:team/repo.git":                             {"work-gl", "team/repo"},
		"https://org@dev.azure.com/org/My%20Proj/_git/repo": {"dev.azure.com", "org/My Proj/_git/repo"},
		"git@ssh.dev.azure.com:v3/org/proj/repo":            {"dev.azure.com", "org/proj/_git/repo"},
		"org@vs-ssh.visualstudio.com:v3/org/proj/repo":      {"org.visualstudio.com", "proj/_git/repo"},
		"https://org.visualstudio.com/proj/_git/repo":       {"org.visualstudio.com", "proj/_git/repo"},
		"ssh://server:22/tfs/Coll/proj/_git/repo":           {"server", "tfs/Coll/proj/_git/repo"},
	}
	for in, want := range cases {
		got, err := ParseRemoteURL(in)
		if err != nil || got.Host != want[0] || got.Path != want[1] {
			t.Errorf("ParseRemoteURL(%q) = %+v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"/local/path", "https://gitlab.com/onlyone", "", `C:\Users\me\origin.git`, "C:/Users/me/origin.git"} {
		if _, err := ParseRemoteURL(bad); err == nil {
			t.Errorf("ParseRemoteURL(%q) should fail", bad)
		}
	}
}

func gitlabForge() *Forge {
	return &Forge{Info: Info{Name: "gitlab", Terms: protocol.Terms{Short: "MR", Long: "merge request", Ref: "!%d"}}}
}

func TestBlockRoundTrip(t *testing.T) {
	info := StackInfo{Trunk: "main", Items: []StackItem{{1, "a"}, {2, "b-->x"}, {3, "c"}}}
	b := gitlabForge().Block(nil)
	block := b.Render(info, 2)
	if strings.Count(block, "-->") != 2 {
		t.Fatalf("branch name leaked a comment terminator:\n%s", block)
	}
	if !strings.Contains(block, "- **!2** ← this MR\n") || !strings.Contains(block, "- !3\n- **!2**") {
		t.Fatalf("unexpected render:\n%s", block)
	}
	desc := SetBlock("User text.", block)
	got, ok := ParseBlock(desc)
	if !ok || got.Trunk != "main" || !reflect.DeepEqual(got.Items, info.Items) || got.Version != 1 {
		t.Fatalf("ParseBlock = %+v, %v", got, ok)
	}
	// Replacing keeps the user's text and leaves exactly one block.
	again := SetBlock(desc, b.Render(info, 3))
	if strings.Count(again, Marker) != 1 || !strings.HasPrefix(again, "User text.\n\n") {
		t.Fatalf("SetBlock did not replace:\n%s", again)
	}
	if StripBlock(again) != "User text." {
		t.Fatalf("StripBlock = %q", StripBlock(again))
	}
	if SetBlock("", block) != block {
		t.Fatal("SetBlock on empty description should be just the block")
	}
	if _, ok := ParseBlock("no block here"); ok {
		t.Fatal("ParseBlock found a block in plain text")
	}
}

func TestStripBlockKeepsParagraphs(t *testing.T) {
	info := StackInfo{Trunk: "main", Items: []StackItem{{1, "a"}}}
	b := gitlabForge().Block(nil)
	desc := "Intro.\n\n" + b.Render(info, 1) + "\n\nCloses #12"
	if got := SetBlock(desc, b.Render(info, 1)); !strings.HasPrefix(got, "Intro.\n\nCloses #12\n\n<!--") {
		t.Fatalf("text around the block was merged into one paragraph:\n%q", got)
	}
	if got := StripBlock(b.Render(info, 1) + "\n\nAfter."); got != "After." {
		t.Fatalf("StripBlock = %q", got)
	}
}

func TestLinkRefBlock(t *testing.T) {
	f := &Forge{Info: Info{Name: "bitbucket", BlockStyle: protocol.BlockLinkRef, Terms: protocol.Terms{Short: "PR"}}}
	b := f.Block(func(n int) string { return "https://bb.example/pr/" + string(rune('0'+n)) })
	info := StackInfo{Trunk: "main", Items: []StackItem{{1, "fix(x)"}, {2, "b"}}}
	block := b.Render(info, 2)
	if !strings.HasPrefix(block, "[//]: # (git-stack:begin ") || strings.Contains(block, "fix(x)") {
		t.Fatalf("unexpected link-ref block:\n%s", block)
	}
	if !strings.Contains(block, "- **[PR #2](https://bb.example/pr/2)** ← this PR\n") {
		t.Fatalf("CRs should be linked by URL:\n%s", block)
	}
	got, ok := ParseBlock("Text\n\n" + block)
	if !ok || !reflect.DeepEqual(got.Items, info.Items) {
		t.Fatalf("ParseBlock = %+v, %v", got, ok)
	}
	if StripBlock("Text\n\n"+block) != "Text" {
		t.Fatalf("StripBlock = %q", StripBlock("Text\n\n"+block))
	}
}

func TestLegacyGlabStaxBlock(t *testing.T) {
	desc := "Hi\n\n<!-- glab-stax:begin {\"v\":1,\"trunk\":\"main\",\"mrs\":[{\"iid\":4,\"branch\":\"a\"},{\"iid\":5,\"branch\":\"b\"}]} -->\n---\n\n- !5\n- **!4**\n<!-- glab-stax:end -->"
	got, ok := ParseBlock(desc)
	if !ok || got.Trunk != "main" || !reflect.DeepEqual(got.Items, []StackItem{{4, "a"}, {5, "b"}}) {
		t.Fatalf("ParseBlock = %+v, %v", got, ok)
	}
	if again := SetBlock(desc, gitlabForge().Block(nil).Render(*got, 4)); strings.Contains(again, "glab-stax") || !strings.HasPrefix(again, "Hi\n\n<!-- git-stack:begin") {
		t.Fatalf("legacy block not replaced:\n%s", again)
	}
}

func TestHostName(t *testing.T) {
	for name, want := range map[string]string{"github": "GitHub", "gitlab": "GitLab", "bitbucket": "Bitbucket", "fake": "fake"} {
		if got := (&Forge{Info: Info{Name: name}}).HostName(); got != want {
			t.Errorf("HostName(%s) = %q, want %q", name, got, want)
		}
	}
}

func TestGuessProvider(t *testing.T) {
	for host, want := range map[string]string{
		"github.com": "github", "GitLab.com": "gitlab", "gitlab.corp.example": "gitlab",
		"github.corp.example": "github", "bitbucket.org": "bitbucket", "dev.azure.com": "ado", "org.visualstudio.com": "ado", "ssh.dev.azure.com": "", "bitbucket.corp.example": "", "git.example": "",
	} {
		if got := GuessProvider(host); got != want {
			t.Errorf("GuessProvider(%q) = %q, want %q", host, got, want)
		}
	}
}

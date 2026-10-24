package forge

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"gitlab.com/malephex/git-stack/pkg/protocol"
)

const (
	// Marker is searched for to find stacks on the host.
	Marker = "git-stack:begin"
	// LegacyMarker starts blocks written by glab-stax, which are still read.
	LegacyMarker = "glab-stax:begin"
	// RepoURL is git-stack's home page, linked from every block.
	RepoURL = "https://gitlab.com/malephex/git-stack"
)

// Both block styles, and blocks written by glab-stax, are recognised whatever
// the provider's style, so a change of style never orphans a block.
var blockRe = regexp.MustCompile(`(?s)\n*(?:` +
	`<!-- (?:git-stack|glab-stax):begin (\{.*?\}) -->.*?<!-- (?:git-stack|glab-stax):end -->` +
	`|\[//\]: # \(git-stack:begin (\{.*?\})\).*?\[//\]: # \(git-stack:end\)` +
	`)\n*`)

// StackInfo is the machine-readable part of the block in each CR description.
type StackInfo struct {
	Version int         `json:"v"`
	Trunk   string      `json:"trunk"`
	Items   []StackItem `json:"crs"` // bottom first
}

// StackItem is one CR of a stack.
type StackItem struct {
	Number int    `json:"n"`
	Branch string `json:"branch"`
}

// UnmarshalJSON also reads the glab-stax format ("mrs" of {"iid", "branch"}).
func (s *StackInfo) UnmarshalJSON(data []byte) error {
	var raw struct {
		Version int         `json:"v"`
		Trunk   string      `json:"trunk"`
		Items   []StackItem `json:"crs"`
		MRs     []struct {
			IID    int    `json:"iid"`
			Branch string `json:"branch"`
		} `json:"mrs"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*s = StackInfo{Version: raw.Version, Trunk: raw.Trunk, Items: raw.Items}
	for _, m := range raw.MRs {
		s.Items = append(s.Items, StackItem{Number: m.IID, Branch: m.Branch})
	}
	return nil
}

// Branches returns the branch of every item, bottom first.
func (s *StackInfo) Branches() []string {
	out := make([]string, len(s.Items))
	for i, it := range s.Items {
		out[i] = it.Branch
	}
	return out
}

// Block renders stack blocks for one forge.
type Block struct {
	Style string // a protocol.Block* constant; "" is BlockHTMLComment
	Short string // e.g. PR
	// Ref writes a reference to CR n, e.g. "!12" or a Markdown link.
	Ref func(n int) string
}

// Block returns the block renderer for f. url returns a CR's web URL, or "",
// for hosts without a short reference syntax.
func (f *Forge) Block(url func(n int) string) Block {
	ref := f.Ref
	if f.Info.Terms.Ref == "" {
		ref = func(n int) string {
			if u := url(n); u != "" {
				return fmt.Sprintf("[%s #%d](%s)", f.Short(), n, u)
			}
			return fmt.Sprintf("#%d", n)
		}
	}
	return Block{Style: f.Info.BlockStyle, Short: f.Short(), Ref: ref}
}

// Render renders the stack block for the CR numbered self.
func (b Block) Render(info StackInfo, self int) string {
	info.Version = 1
	data, _ := json.Marshal(info) // escapes < and >, so an HTML comment cannot be closed early
	var w strings.Builder
	if b.Style == protocol.BlockLinkRef {
		// Parentheses would end the link title; JSON can spell them as escapes.
		js := strings.NewReplacer("(", jsonEscape('('), ")", jsonEscape(')')).Replace(string(data))
		w.WriteString("[//]: # (" + Marker + " " + js + ")\n\n")
	} else {
		w.WriteString("<!-- " + Marker + " " + string(data) + " -->\n")
	}
	w.WriteString("---\n\n")
	fmt.Fprintf(&w, "**Stack** (managed by [git stack](%s))\n\n", RepoURL)
	for i := len(info.Items) - 1; i >= 0; i-- {
		it := info.Items[i]
		if it.Number == self {
			fmt.Fprintf(&w, "- **%s** ← this %s\n", b.Ref(it.Number), b.Short)
		} else {
			fmt.Fprintf(&w, "- %s\n", b.Ref(it.Number))
		}
	}
	fmt.Fprintf(&w, "- `%s`\n", info.Trunk)
	if b.Style == protocol.BlockLinkRef {
		w.WriteString("\n[//]: # (git-stack:end)")
	} else {
		w.WriteString("<!-- git-stack:end -->")
	}
	return w.String()
}

// ParseBlock extracts stack info from a CR description.
func ParseBlock(desc string) (*StackInfo, bool) {
	m := blockRe.FindStringSubmatch(desc)
	if m == nil {
		return nil, false
	}
	js := m[1]
	if js == "" {
		js = m[2]
	}
	var info StackInfo
	if err := json.Unmarshal([]byte(js), &info); err != nil {
		return nil, false
	}
	return &info, true
}

// SetBlock replaces the stack block in desc, or appends it.
func SetBlock(desc, block string) string {
	base := StripBlock(desc)
	if base == "" {
		return block
	}
	return base + "\n\n" + block
}

// StripBlock removes the stack block from desc. Text before and after a block
// in the middle stays in separate paragraphs.
func StripBlock(desc string) string {
	var b strings.Builder
	last := 0
	for _, m := range blockRe.FindAllStringIndex(desc, -1) {
		b.WriteString(desc[last:m[0]])
		if m[0] > 0 && m[1] < len(desc) {
			b.WriteString("\n\n")
		}
		last = m[1]
	}
	b.WriteString(desc[last:])
	return strings.TrimRight(b.String(), "\n ")
}

// jsonEscape spells r as a JSON \uXXXX escape.
func jsonEscape(r rune) string { return fmt.Sprintf(`\%c%04x`, 'u', r) }

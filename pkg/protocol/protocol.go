// Package protocol is the contract between git-stack and its provider
// plugins: executables named git-stack-<provider> that manage pull requests
// (or merge requests) on one kind of Git host.
//
// git-stack runs a provider once per operation as
//
//	git-stack-<provider> <op>
//
// with a JSON Request on stdin, and reads a JSON Response from stdout. A
// failed operation exits non-zero with Response.Error set. Providers written in
// Go implement Provider and call Serve from main.
package protocol

import (
	"errors"
	"fmt"
)

// Version is the protocol version spoken by this package. git-stack refuses
// providers that report a different major version in Describe.
const Version = 1

// ExecutablePrefix is prepended to a provider name to get its executable.
const ExecutablePrefix = "git-stack-"

// Operation names (the first argument of a provider executable).
const (
	OpDescribe         = "describe"
	OpDetect           = "detect"
	OpGetCR            = "get_cr"
	OpFindCRForBranch  = "find_cr_for_branch"
	OpSearchCRs        = "search_crs"
	OpListCRsTargeting = "list_crs_targeting"
	OpCreateCR         = "create_cr"
	OpUpdateCR         = "update_cr"
	OpMarkReady        = "mark_ready"
	OpCancelAutoMerge  = "cancel_auto_merge"
	OpMerge            = "merge"
	OpGetBranch        = "get_branch"
	OpGetRepo          = "get_repo"
	OpSyncStack        = "stack_sync"
	OpUnstack          = "stack_unstack"
)

// Repo identifies the repository an operation works on.
type Repo struct {
	// RemoteURL is the git remote URL as configured.
	RemoteURL string `json:"remote_url"`
	// Host is the API host, e.g. github.com, with ssh config aliases resolved.
	Host string `json:"host"`
	// Path is the repository path on the host, e.g. owner/repo or group/sub/repo.
	Path string `json:"path"`
}

// State of a change request.
const (
	StateOpen    = "open"
	StateClosed  = "closed"
	StateMerged  = "merged"
	StateMerging = "merging" // the host is performing the merge right now
)

// CR is a change request: a pull request or merge request.
type CR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	Draft  bool   `json:"draft,omitempty"`
	// Head is the source branch, Base the target branch.
	Head string `json:"head"`
	Base string `json:"base"`
	URL  string `json:"url"`
	// HeadSHA is the commit the head branch is (or, once merged, was) at.
	HeadSHA string `json:"head_sha,omitempty"`
	// AutoMerge reports that the CR merges by itself once checks pass.
	AutoMerge bool `json:"auto_merge,omitempty"`
	// DeleteHead reports that the head branch should be deleted after merging.
	DeleteHead bool `json:"delete_head,omitempty"`
	// Checking reports that the host is still working out mergeability.
	Checking bool `json:"checking,omitempty"`
	// MergeError is the host's reason a merge did not happen, if any.
	MergeError string `json:"merge_error,omitempty"`
	// FromFork reports that Head lives in another repository.
	FromFork bool `json:"from_fork,omitempty"`
}

// Open reports whether the CR is open.
func (c *CR) Open() bool { return c.State == StateOpen }

// Merged reports whether the CR has been merged.
func (c *CR) Merged() bool { return c.State == StateMerged }

// NewCR describes a change request to create.
type NewCR struct {
	Head  string `json:"head"`
	Base  string `json:"base"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Draft bool   `json:"draft,omitempty"`
}

// Update lists the attributes to change; nil fields are left alone.
type Update struct {
	Base  *string `json:"base,omitempty"`
	Title *string `json:"title,omitempty"`
	Body  *string `json:"body,omitempty"`
}

// Branch is what git-stack needs to know about a branch on the host.
type Branch struct {
	Protected bool `json:"protected"`
	Default   bool `json:"default"`
}

// RepoSettings are the repository settings git-stack uses.
type RepoSettings struct {
	// DefaultMergeMethod is merge, squash or rebase: what the repository
	// prefers when the user has not chosen.
	DefaultMergeMethod string `json:"default_merge_method"`
}

// Merge methods.
const (
	MethodMerge  = "merge"
	MethodSquash = "squash"
	MethodRebase = "rebase"
)

// Block styles: how the machine-readable stack data is hidden in descriptions.
const (
	BlockHTMLComment = "html-comment" // <!-- git-stack:begin {...} -->
	BlockLinkRef     = "link-ref"     // [//]: # (git-stack:begin {...})
)

// Terms name change requests in the host's own words.
type Terms struct {
	Short string `json:"short"` // PR, MR
	Long  string `json:"long"`  // pull request, merge request
	// Ref formats a CR number in text, with %d for the number: "#%d", "!%d".
	Ref string `json:"ref"`
}

// Info describes a provider. It does not depend on a repository.
type Info struct {
	Name     string `json:"name"`
	Protocol int    `json:"protocol"`
	Terms    Terms  `json:"terms"`
	// BlockStyle is one of the Block* constants; empty means BlockHTMLComment.
	BlockStyle string `json:"block_style,omitempty"`
	// MaxBody is the longest description the host accepts; 0 means no limit.
	MaxBody int `json:"max_body,omitempty"`
	// MergeMethods lists the methods Merge supports.
	MergeMethods []string `json:"merge_methods,omitempty"`
	// NativeStacks reports that SyncStack and Unstack manage a stack object
	// on the host, beyond the block git-stack keeps in each description.
	NativeStacks bool `json:"native_stacks,omitempty"`
}

// Provider manages change requests for one repository.
type Provider interface {
	// GetCR fetches a CR by number.
	GetCR(number int) (*CR, error)
	// FindCRForBranch returns the open CR whose head is branch, else the most
	// recently merged one, else nil. CRs from forks are ignored.
	FindCRForBranch(branch string) (*CR, error)
	// SearchCRs returns open CRs whose description contains text.
	SearchCRs(text string) ([]*CR, error)
	// ListCRsTargeting returns open CRs, not from forks, whose base is branch.
	ListCRsTargeting(branch string) ([]*CR, error)
	// CreateCR opens a CR.
	CreateCR(n NewCR) (*CR, error)
	// UpdateCR changes the attributes set in u.
	UpdateCR(number int, u Update) (*CR, error)
	// MarkReady takes a CR out of draft.
	MarkReady(number int) (*CR, error)
	// CancelAutoMerge turns auto-merge off.
	CancelAutoMerge(number int) error
	// Merge asks the host to merge the CR with method once head is at sha.
	// The host may merge at once, or set the CR to merge when checks pass
	// (AutoMerge set in the result). The head branch is never deleted.
	Merge(number int, method, sha string) (*CR, error)
	// GetBranch fetches a branch.
	GetBranch(name string) (*Branch, error)
	// GetRepo fetches repository settings.
	GetRepo() (*RepoSettings, error)
	// SyncStack makes the host's native stack hold exactly numbers, bottom
	// first. Providers without native stacks return nil.
	SyncStack(numbers []int) error
	// Unstack removes the CRs from the host's native stack, if any.
	Unstack(numbers []int) error
}

// Error codes.
const (
	CodeUnsupported = "unsupported" // the provider cannot do this
	CodeNotFound    = "not_found"
	CodeAuth        = "auth" // not logged in, or not allowed
	CodeFailure     = "failure"
)

// Error is a provider failure with a machine-readable code.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Errorf returns an *Error with code.
func Errorf(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf returns the code of err, CodeFailure if it carries none.
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeFailure
}

// Request is the JSON sent on stdin. Fields not used by an operation are omitted.
type Request struct {
	Protocol int     `json:"protocol"`
	Repo     Repo    `json:"repo"`
	Number   int     `json:"number,omitempty"`
	Numbers  []int   `json:"numbers,omitempty"`
	Branch   string  `json:"branch,omitempty"`
	Text     string  `json:"text,omitempty"`
	Method   string  `json:"method,omitempty"`
	SHA      string  `json:"sha,omitempty"`
	New      *NewCR  `json:"new,omitempty"`
	Update   *Update `json:"update,omitempty"`
}

// Response is the JSON written to stdout.
type Response struct {
	Error  *Error        `json:"error,omitempty"`
	Info   *Info         `json:"info,omitempty"`
	Match  bool          `json:"match,omitempty"`
	CR     *CR           `json:"cr,omitempty"`
	CRs    []*CR         `json:"crs,omitempty"`
	Branch *Branch       `json:"branch,omitempty"`
	Repo   *RepoSettings `json:"repo,omitempty"`
}

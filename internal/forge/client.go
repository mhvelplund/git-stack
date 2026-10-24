package forge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/mhvelplund/git-stack/pkg/protocol"
)

// Transport runs one operation of a provider plugin.
type Transport func(op string, req protocol.Request) (protocol.Response, error)

// ExecTransport runs the plugin executable at path.
func ExecTransport(path string) Transport {
	return func(op string, req protocol.Request) (protocol.Response, error) {
		var in bytes.Buffer
		if err := json.NewEncoder(&in).Encode(req); err != nil {
			return protocol.Response{}, err
		}
		cmd := exec.Command(path, op)
		cmd.Stdin = &in
		cmd.Env = append(os.Environ(), "GIT_STACK_PROTOCOL="+fmt.Sprint(protocol.Version))
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		runErr := cmd.Run()
		var resp protocol.Response
		if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if runErr == nil {
				runErr = err
			}
			if msg == "" {
				msg = runErr.Error()
			}
			return protocol.Response{}, fmt.Errorf("provider %s %s: %s", path, op, msg)
		}
		return resp, nil
	}
}

// LocalTransport runs p in this process, through the same JSON encoding as
// ExecTransport. Tests use it, and so can built-in providers.
func LocalTransport(p protocol.Plugin) Transport {
	return func(op string, req protocol.Request) (protocol.Response, error) {
		var in bytes.Buffer
		if err := json.NewEncoder(&in).Encode(req); err != nil {
			return protocol.Response{}, err
		}
		resp := protocol.Dispatch(p, op, &in)
		// Round-trip the response too, so tests see what a plugin would send.
		data, err := json.Marshal(resp)
		if err != nil {
			return protocol.Response{}, err
		}
		var out protocol.Response
		return out, json.Unmarshal(data, &out)
	}
}

// Describe asks a plugin for its Info.
func Describe(t Transport) (*protocol.Info, error) {
	resp, err := call(t, protocol.OpDescribe, protocol.Request{Protocol: protocol.Version})
	if err != nil {
		return nil, err
	}
	if resp.Info == nil {
		return nil, errors.New("provider sent no info")
	}
	if resp.Info.Protocol != protocol.Version {
		return nil, fmt.Errorf("provider %s speaks protocol %d, git-stack speaks %d", resp.Info.Name, resp.Info.Protocol, protocol.Version)
	}
	return resp.Info, nil
}

// Detect asks a plugin whether repo is hosted where it works.
func Detect(t Transport, repo protocol.Repo) bool {
	resp, err := call(t, protocol.OpDetect, protocol.Request{Protocol: protocol.Version, Repo: repo})
	return err == nil && resp.Match
}

func call(t Transport, op string, req protocol.Request) (protocol.Response, error) {
	resp, err := t(op, req)
	if err != nil {
		return resp, err
	}
	if resp.Error != nil {
		return resp, resp.Error
	}
	return resp, nil
}

// client is a protocol.Provider that forwards every call to a plugin.
type client struct {
	t    Transport
	repo protocol.Repo
}

var _ protocol.Provider = (*client)(nil)

func (c *client) do(op string, req protocol.Request) (protocol.Response, error) {
	req.Protocol = protocol.Version
	req.Repo = c.repo
	return call(c.t, op, req)
}

func (c *client) cr(op string, req protocol.Request) (*protocol.CR, error) {
	resp, err := c.do(op, req)
	return resp.CR, err
}

func (c *client) GetCR(n int) (*protocol.CR, error) {
	return c.cr(protocol.OpGetCR, protocol.Request{Number: n})
}

func (c *client) FindCRForBranch(branch string) (*protocol.CR, error) {
	return c.cr(protocol.OpFindCRForBranch, protocol.Request{Branch: branch})
}

func (c *client) SearchCRs(text string) ([]*protocol.CR, error) {
	resp, err := c.do(protocol.OpSearchCRs, protocol.Request{Text: text})
	return resp.CRs, err
}

func (c *client) ListCRsTargeting(branch string) ([]*protocol.CR, error) {
	resp, err := c.do(protocol.OpListCRsTargeting, protocol.Request{Branch: branch})
	return resp.CRs, err
}

func (c *client) CreateCR(n protocol.NewCR) (*protocol.CR, error) {
	return c.cr(protocol.OpCreateCR, protocol.Request{New: &n})
}

func (c *client) UpdateCR(n int, u protocol.Update) (*protocol.CR, error) {
	return c.cr(protocol.OpUpdateCR, protocol.Request{Number: n, Update: &u})
}

func (c *client) MarkReady(n int) (*protocol.CR, error) {
	return c.cr(protocol.OpMarkReady, protocol.Request{Number: n})
}

func (c *client) CancelAutoMerge(n int) error {
	_, err := c.do(protocol.OpCancelAutoMerge, protocol.Request{Number: n})
	return err
}

func (c *client) Merge(n int, method, sha string) (*protocol.CR, error) {
	return c.cr(protocol.OpMerge, protocol.Request{Number: n, Method: method, SHA: sha})
}

func (c *client) GetBranch(name string) (*protocol.Branch, error) {
	resp, err := c.do(protocol.OpGetBranch, protocol.Request{Branch: name})
	return resp.Branch, err
}

func (c *client) GetRepo() (*protocol.RepoSettings, error) {
	resp, err := c.do(protocol.OpGetRepo, protocol.Request{})
	return resp.Repo, err
}

func (c *client) SyncStack(numbers []int) error {
	_, err := c.do(protocol.OpSyncStack, protocol.Request{Numbers: numbers})
	return err
}

func (c *client) Unstack(numbers []int) error {
	_, err := c.do(protocol.OpUnstack, protocol.Request{Numbers: numbers})
	return err
}

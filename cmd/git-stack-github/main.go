// Command git-stack-github is git-stack's GitHub provider plugin. It uses the
// login and host configuration of the gh CLI.
package main

import (
	"github.com/cli/go-gh/v2/pkg/api"

	"gitlab.com/malephex/git-stack/internal/provider/github"
	"gitlab.com/malephex/git-stack/pkg/protocol"
)

func main() {
	protocol.Serve(github.Plugin(api.ClientOptions{}))
}

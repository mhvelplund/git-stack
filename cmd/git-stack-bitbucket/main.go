// Command git-stack-bitbucket is git-stack's Bitbucket Cloud provider plugin.
// It reads credentials from BITBUCKET_TOKEN, BITBUCKET_EMAIL and
// BITBUCKET_API_TOKEN, or git's credential helpers.
package main

import (
	"gitlab.com/malephex/git-stack/internal/provider/bitbucket"
	"gitlab.com/malephex/git-stack/pkg/protocol"
)

func main() {
	protocol.Serve(bitbucket.Plugin(bitbucket.Options{}))
}

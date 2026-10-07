package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/JuliusBrussee/caveman/proxy/internal/pool"
	"github.com/JuliusBrussee/caveman/shared/platform/env"
)

// runProviderLogin is `caveman providers login <provider>`: the subscription
// logins whose terms allow a third-party client. Only Sign in with ChatGPT
// today (experimental); API keys are added by the CLI itself.
func runProviderLogin(args []string) {
	if len(args) != 1 || args[0] != "chatgpt" {
		fmt.Fprintln(os.Stderr, "usage: caveman-proxy provider-login chatgpt")
		os.Exit(2)
	}
	home := env.String("CAVEMAN_HOME", "")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "caveman: no home directory")
			os.Exit(1)
		}
		home = filepath.Join(userHome, ".caveman")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := pool.NewStore(home).LoginChatGPT(ctx, pool.LoginOptions{Out: os.Stderr}); err != nil {
		fmt.Fprintln(os.Stderr, "caveman: "+err.Error())
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "Signed in with ChatGPT. Routing may now send requests to your ChatGPT plan, straight from this machine.")
}

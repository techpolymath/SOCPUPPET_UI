// Run this smoke client inside the app container after building it on the host.
// It uses the application's actual provider implementation and waits for the
// active chat agent to supply each response in the bridge mailbox.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/techpolymath/SOCPUPPET_UI/agent"
)

func main() {
	cfg := agent.ConfigFrom("ollama", "session-bridge", "http://127.0.0.1:8790/v1", "", "")
	cfg.Stream = false
	provider, err := cfg.NewProvider()
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	req := llm.CompletionRequest{Messages: []llm.Message{llm.UserText("Reply with exactly BRIDGE_OK to confirm this request reached the current chat session.")}}
	if len(os.Args) > 1 && os.Args[1] == "stream" {
		for event, err := range provider.Stream(ctx, req) {
			if err != nil {
				panic(err)
			}
			fmt.Printf("%+v\n", event)
		}
	} else {
		message, reason, _, err := provider.Complete(ctx, req)
		if err != nil {
			panic(err)
		}
		fmt.Printf("message=%+v finish=%s\n", message, reason)
	}
}

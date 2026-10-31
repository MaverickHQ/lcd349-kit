package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MaverickHQ/lcd349-kit/internal/mcpserver"
)

func init() {
	register(&command{name: "mcp", usage: "[--root dir]", summary: "serve the tools over MCP on stdin and stdout (the Claude Code plugin runs this)", run: cmdMCP})
}

func cmdMCP(c *cmdCtx) int {
	root := c.flags.String("root", "", "the workspace `directory` (default: the working directory)")
	if code, ok := c.parse(); !ok {
		return code
	}
	s, err := mcpserver.New(mcpserver.Options{Root: *root})
	if err != nil {
		return c.fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	// The session ends when the client closes stdin; the instances started
	// here stop with it (R7.6).
	if err := s.Serve(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		return c.fail(err)
	}
	return exitOK
}

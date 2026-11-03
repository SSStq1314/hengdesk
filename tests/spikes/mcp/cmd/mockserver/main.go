package main

import (
	"flag"
	"log"

	"github.com/mark3labs/mcp-go/server"
	"hengdesk-spikes/mcp/mock"
)

func main() {
	addr := flag.String("addr", ":8931", "listen addr")
	sse := flag.Bool("sse", false, "use legacy SSE transport instead of streamable HTTP")
	flag.Parse()
	m := mock.New()
	if *sse {
		log.Printf("SSE MCP on %s/sse", *addr)
		log.Fatal(server.NewSSEServer(m.MCP).Start(*addr))
	}
	log.Printf("streamable HTTP MCP on %s/mcp", *addr)
	log.Fatal(server.NewStreamableHTTPServer(m.MCP).Start(*addr))
}

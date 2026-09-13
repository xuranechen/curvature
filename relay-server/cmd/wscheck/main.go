// Command wscheck opens a WebSocket through the relay's /n/{node}/ path and
// verifies an echo round-trip.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	nodeID := flag.String("node", "", "node_id to connect through")
	base := flag.String("base", "http://127.0.0.1:8080", "relay base URL")
	flag.Parse()
	if *nodeID == "" {
		log.Fatal("-node required")
	}
	wsURL := *base + "/n/" + *nodeID + "/echo"
	if len(*base) > 4 && (*base)[:4] == "http" {
		wsURL = "ws" + (*base)[4:] + "/n/" + *nodeID + "/echo"
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		log.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, []byte("ping-through-relay")); err != nil {
		log.Fatalf("write: %v", err)
	}
	_, msg, err := conn.ReadMessage()
	if err != nil {
		log.Fatalf("read: %v", err)
	}
	fmt.Printf("ECHO OK: %s\n", msg)
	if string(msg) != "ping-through-relay" {
		fmt.Fprintln(os.Stderr, "mismatch!")
		os.Exit(1)
	}
}

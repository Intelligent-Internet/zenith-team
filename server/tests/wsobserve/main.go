// Command wsobserve is the e2e smoke observer: it connects two clients to the
// boardly client WebSocket and prints one line per received frame
// (<client> <unix-ms> <event type>), proving multi-socket fanout ordering.
//
// Usage: wsobserve 'ws://host/api/v1/ws?company_id=<uuid>' <bearer-token>
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: wsobserve <ws-url> <bearer-token>")
		os.Exit(2)
	}
	url, token := os.Args[1], os.Args[2]
	for i := range 2 {
		go observe(i, url, token)
	}
	select {}
}

func observe(client int, url, token string) {
	header := http.Header{"Authorization": {"Bearer " + token}}
	conn, resp, err := websocket.DefaultDialer.Dial(url, header)
	if resp != nil && resp.Body != nil {
		// Nothing here reads the handshake response, but a rejected upgrade
		// returns a real body that holds the connection open until it is closed.
		_ = resp.Body.Close()
	}
	if err != nil {
		fmt.Printf("c%d dial-error %v\n", client, err)
		os.Exit(1)
	}
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			fmt.Printf("c%d read-error %v\n", client, err)
			return
		}
		var frame struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(data, &frame)
		fmt.Printf("c%d %d %s\n", client, time.Now().UnixMilli(), frame.Type)
	}
}

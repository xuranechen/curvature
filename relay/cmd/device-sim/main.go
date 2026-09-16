// Command device-sim simulates the Curvature device connector (client side of
// the relay protocol) so the relay server can be tested end-to-end without
// building the full Curvature binary.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

func main() {
	relayBase := flag.String("base", "http://localhost:8080", "relay base URL")
	code := flag.String("code", "pc_sim_"+fmt.Sprint(time.Now().UnixNano()), "pending bind code")
	deviceID := flag.String("device", "md_sim_device_001", "X-Curvature-Device-ID header")
	flag.Parse()

	if !strings.HasPrefix(*code, "pc_") {
		log.Fatal("code must start with pc_")
	}

	localSrv := &http.Server{Addr: "127.0.0.1:17331", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "hello from local:%s", r.URL.RequestURI())
	})}
	go func() { _ = localSrv.ListenAndServe() }()

	echoSrv := &http.Server{Addr: "127.0.0.1:17332", Handler: websocketEchoHandler()}
	go func() { _ = echoSrv.ListenAndServe() }()
	time.Sleep(200 * time.Millisecond)

	poll(*relayBase, *code, *deviceID, "")
	time.Sleep(300 * time.Millisecond)

	confirm(*relayBase, *code)

	creds := poll(*relayBase, *code, *deviceID, "")
	log.Printf("credentials: endpoint=%v node_id=%v", creds["endpoint"], creds["node_id"])

	endpoint, _ := creds["endpoint"].(string)
	deviceToken, _ := creds["device_token"].(string)
	conn, _, err := websocket.DefaultDialer.Dial(endpoint, http.Header{
		"Authorization": []string{"Bearer " + deviceToken},
	})
	if err != nil {
		log.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	ycfg := yamux.DefaultConfig()
	ycfg.MaxStreamWindowSize = 4 << 20
	ycfg.ConnectionWriteTimeout = 60 * time.Second
	ycfg.EnableKeepAlive = true
	ycfg.KeepAliveInterval = 30 * time.Second
	sess, err := yamux.Client(newWSConn(conn), ycfg)
	if err != nil {
		log.Fatalf("yamux: %v", err)
	}
	defer sess.Close()

	log.Printf("connected, accepting relay streams...")
	for {
		stream, err := sess.Accept()
		if err != nil {
			log.Printf("accept stream ended: %v", err)
			return
		}
		go handleStream(stream)
	}
}

func websocketEchoHandler() http.Handler {
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			_ = conn.WriteMessage(mt, msg)
		}
	})
}

func handleStream(stream net.Conn) {
	defer stream.Close()
	req, err := http.ReadRequest(bufio.NewReader(stream))
	if err != nil {
		log.Printf("read request: %v", err)
		return
	}
	log.Printf("relayed request: %s %s (upgrade=%v)", req.Method, req.URL.RequestURI(), websocket.IsWebSocketUpgrade(req))

	if websocket.IsWebSocketUpgrade(req) {
		proxyWS(req, stream)
		return
	}

	out := req.Clone(req.Context())
	out.URL.Scheme = "http"
	out.URL.Host = "127.0.0.1:17331"
	out.RequestURI = ""
	out.Host = out.URL.Host

	resp, err := http.DefaultClient.Do(out)
	if err != nil {
		writeErr(stream, 502, "local_failed")
		return
	}
	defer resp.Body.Close()
	if err := resp.Write(stream); err != nil {
		log.Printf("write response: %v", err)
	}
}

// proxyWS mirrors the Curvature connector WebSocket proxying: dial the local
// echo server, write the 101 response, then bridge both directions using the
// relay framing format.
func proxyWS(req *http.Request, stream net.Conn) {
	target := "ws://127.0.0.1:17332"
	headers := http.Header{}
	headers.Set("Origin", "http://127.0.0.1:17332")
	localConn, resp, err := websocket.DefaultDialer.Dial(target, headers)
	if err != nil {
		if resp != nil {
			_ = resp.Write(stream)
		}
		writeErr(stream, 502, "local_ws_failed")
		return
	}
	defer localConn.Close()
	if resp == nil {
		writeErr(stream, 502, "local_ws_no_response")
		return
	}
	if err := resp.Write(stream); err != nil {
		return
	}

	errCh := make(chan error, 2)
	go simBridgeWS2Stream(localConn, stream, errCh)
	go simBridgeStream2WS(stream, localConn, errCh)
	<-errCh
	_ = writeWSCloseFrame(stream, websocket.CloseNormalClosure, "connector_closed")
}

func simBridgeWS2Stream(localConn *websocket.Conn, stream io.Writer, errCh chan<- error) {
	for {
		mt, payload, err := localConn.ReadMessage()
		if err != nil {
			code, text := websocket.CloseNormalClosure, "local_closed"
			if ce, ok := err.(*websocket.CloseError); ok {
				code, text = ce.Code, ce.Text
			}
			_ = writeWSCloseFrame(stream, sanitizeCloseCode(code), text)
			errCh <- nil
			return
		}
		if err := writeWSDataFrame(stream, mt, payload); err != nil {
			errCh <- err
			return
		}
	}
}

func simBridgeStream2WS(stream io.Reader, localConn *websocket.Conn, errCh chan<- error) {
	for {
		ft, opcode, payload, closeCode, closeText, err := readWSFrame(stream)
		if err != nil {
			errCh <- err
			return
		}
		switch ft {
		case 1:
			_ = localConn.WriteMessage(opcode, payload)
		case 2:
			_ = localConn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(sanitizeCloseCode(closeCode), closeText), time.Now().Add(2*time.Second))
			errCh <- nil
			return
		}
	}
}

const (
	simFrameData  byte = 1
	simFrameClose byte = 2
)

func writeWSDataFrame(w io.Writer, opcode int, payload []byte) error {
	hdr := make([]byte, 6)
	hdr[0] = simFrameData
	hdr[1] = byte(opcode)
	putUint32(hdr[2:], uint32(len(payload)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

func writeWSCloseFrame(w io.Writer, code int, reason string) error {
	rb := []byte(reason)
	if len(rb) > 65535 {
		rb = rb[:65535]
	}
	hdr := make([]byte, 7)
	hdr[0] = simFrameClose
	hdr[1] = byte(code >> 8)
	hdr[2] = byte(code)
	putUint32(hdr[3:], uint32(len(rb)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if len(rb) == 0 {
		return nil
	}
	_, err := w.Write(rb)
	return err
}

func readWSFrame(r io.Reader) (frameType byte, opcode int, payload []byte, closeCode int, closeText string, err error) {
	var kind [1]byte
	if _, err = io.ReadFull(r, kind[:]); err != nil {
		return 0, 0, nil, 0, "", err
	}
	switch kind[0] {
	case simFrameData:
		var hdr [5]byte
		if _, err = io.ReadFull(r, hdr[:]); err != nil {
			return 0, 0, nil, 0, "", err
		}
		opcode = int(hdr[0])
		size := uint32(hdr[1])<<24 | uint32(hdr[2])<<16 | uint32(hdr[3])<<8 | uint32(hdr[4])
		payload = make([]byte, size)
		if _, err = io.ReadFull(r, payload); err != nil {
			return 0, 0, nil, 0, "", err
		}
		return simFrameData, opcode, payload, 0, "", nil
	case simFrameClose:
		var hdr [6]byte
		if _, err = io.ReadFull(r, hdr[:]); err != nil {
			return 0, 0, nil, 0, "", err
		}
		closeCode = int(hdr[0])<<8 | int(hdr[1])
		size := uint32(hdr[2])<<24 | uint32(hdr[3])<<16 | uint32(hdr[4])<<8 | uint32(hdr[5])
		reason := make([]byte, size)
		if _, err = io.ReadFull(r, reason); err != nil {
			return 0, 0, nil, 0, "", err
		}
		return simFrameClose, 0, nil, closeCode, string(reason), nil
	default:
		return 0, 0, nil, 0, "", fmt.Errorf("unknown_ws_frame %d", kind[0])
	}
}

func putUint32(b []byte, v uint32) {
	b[0] = byte(v >> 24)
	b[1] = byte(v >> 16)
	b[2] = byte(v >> 8)
	b[3] = byte(v)
}

func sanitizeCloseCode(code int) int {
	switch code {
	case websocket.CloseNoStatusReceived, websocket.CloseAbnormalClosure, websocket.CloseTLSHandshake:
		return websocket.CloseNormalClosure
	default:
		return code
	}
}

func writeErr(stream io.Writer, status int, code string) {
	resp := &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"` + code + `"}`)),
	}
	_ = resp.Write(stream)
}

func poll(base, code, deviceID, purpose string) map[string]any {
	req, _ := http.NewRequest("GET", base+"/api/bind/poll?code="+code+"&purpose="+purpose, nil)
	req.Header.Set("X-Curvature-Device-ID", deviceID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("poll: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		log.Fatalf("poll decode: %v", err)
	}
	b, _ := json.Marshal(out)
	log.Printf("poll -> %s", b)
	return out
}

func confirm(base, code string) map[string]any {
	resp, err := http.Post(base+"/api/bind/confirm?code="+code, "application/json", nil)
	if err != nil {
		log.Fatalf("confirm: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		log.Fatalf("confirm decode: %v", err)
	}
	b, _ := json.Marshal(out)
	log.Printf("confirm -> %s", b)
	return out
}

// wsConn adapts a gorilla websocket connection to net.Conn (mirrors Curvature).
type wsConn struct {
	conn *websocket.Conn
	r    io.Reader
}

func newWSConn(conn *websocket.Conn) net.Conn {
	return &wsConn{conn: conn}
}

func (c *wsConn) Read(p []byte) (int, error) {
	for {
		if c.r == nil {
			mt, reader, err := c.conn.NextReader()
			if err != nil {
				return 0, err
			}
			if mt != websocket.BinaryMessage {
				continue
			}
			c.r = reader
		}
		n, err := c.r.Read(p)
		if err == io.EOF {
			c.r = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *wsConn) Write(p []byte) (int, error) {
	w, err := c.conn.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return 0, err
	}
	n, werr := w.Write(p)
	cerr := w.Close()
	if werr != nil {
		return n, werr
	}
	return n, cerr
}

func (c *wsConn) Close() error         { return c.conn.Close() }
func (c *wsConn) LocalAddr() net.Addr  { return c.conn.LocalAddr() }
func (c *wsConn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }
func (c *wsConn) SetDeadline(t time.Time) error {
	_ = c.conn.SetReadDeadline(t)
	return c.conn.SetWriteDeadline(t)
}
func (c *wsConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *wsConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

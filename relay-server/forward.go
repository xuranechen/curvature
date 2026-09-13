package main

import (
	"bufio"
	"errors"
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

var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"TE",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

func isHopByHop(name string) bool {
	for _, h := range hopByHopHeaders {
		if strings.EqualFold(name, h) {
			return true
		}
	}
	return false
}

type Forwarder struct {
	hub   *Hub
	store *Store
}

func NewForwarder(hub *Hub, store *Store) *Forwarder {
	return &Forwarder{hub: hub, store: store}
}

// handleNode forwards requests under /n/{node_id}/... to the connected device.
func (f *Forwarder) handleNode(w http.ResponseWriter, r *http.Request) {
	const prefix = "/n/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeJSONError(w, http.StatusNotFound, "node_not_found")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	if rest == "" {
		writeJSONError(w, http.StatusNotFound, "node_not_found")
		return
	}
	nodeID, suffix, _ := strings.Cut(rest, "/")
	if nodeID == "" {
		writeJSONError(w, http.StatusNotFound, "node_not_found")
		return
	}
	path := "/" + suffix
	targetURI := path
	if r.URL.RawQuery != "" {
		targetURI += "?" + r.URL.RawQuery
	}
	f.forward(w, r, nodeID, targetURI, "")
}

// handleServiceHost routes subdomain-style requests:
// {slug}-{node_id}-relay.{host}/...
func (f *Forwarder) handleServiceHost(w http.ResponseWriter, r *http.Request) bool {
	hostname := r.Host
	if h, _, err := net.SplitHostPort(hostname); err == nil {
		hostname = h
	}
	subdomain := ""
	if idx := strings.IndexByte(hostname, '.'); idx > 0 {
		subdomain = hostname[:idx]
	}
	if !strings.HasSuffix(subdomain, "-relay") {
		return false
	}
	core := strings.TrimSuffix(subdomain, "-relay")
	idx := strings.LastIndexByte(core, '-')
	if idx <= 0 || idx == len(core)-1 {
		writeJSONError(w, http.StatusNotFound, "service_not_found")
		return true
	}
	slug, nodeID := core[:idx], core[idx+1:]
	log.Printf("[relay] service-host: host=%q subdomain=%q slug=%q node_id=%q", r.Host, subdomain, slug, nodeID)
	if !ValidServiceSlug(slug) {
		writeJSONError(w, http.StatusNotFound, "service_not_found")
		return true
	}
	svc := f.store.getServiceFold(slug, nodeID)
	if svc == nil {
		writeJSONError(w, http.StatusNotFound, "service_not_found")
		return true
	}
	if !svc.Enabled {
		writeJSONError(w, http.StatusForbidden, "service_disabled")
		return true
	}
	targetURI := r.URL.RequestURI()
	f.forward(w, r, nodeID, targetURI, slug)
	return true
}

// forward sends a single HTTP request over a yamux stream to the device.
func (f *Forwarder) forward(w http.ResponseWriter, r *http.Request, nodeID, targetURI, serviceSlug string) {
	sess := f.hub.get(nodeID)
	if sess == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "node_offline")
		return
	}
	if serviceSlug != "" {
		r.Header.Set(serviceSlugHeader, serviceSlug)
	}
	if websocket.IsWebSocketUpgrade(r) {
		f.forwardWebSocket(w, r, sess, targetURI)
		return
	}
	f.forwardHTTP(w, r, sess, targetURI)
}

func (f *Forwarder) forwardHTTP(w http.ResponseWriter, r *http.Request, sess *yamux.Session, targetURI string) {
	stream, err := sess.OpenStream()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "connector_unavailable")
		return
	}
	defer stream.Close()

	if err := writeHTTPRequest(stream, r, targetURI); err != nil {
		writeJSONError(w, http.StatusBadGateway, "connector_write_failed")
		return
	}

	resp, err := http.ReadResponse(bufio.NewReader(stream), r)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "connector_read_failed")
		return
	}
	defer resp.Body.Close()
	copyHTTPResponse(w, resp)
}

func (f *Forwarder) forwardWebSocket(w http.ResponseWriter, r *http.Request, sess *yamux.Session, targetURI string) {
	stream, err := sess.OpenStream()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "connector_unavailable")
		return
	}
	defer stream.Close()

	if err := writeHTTPRequest(stream, r, targetURI); err != nil {
		writeJSONError(w, http.StatusBadGateway, "connector_write_failed")
		return
	}

	br := bufio.NewReader(stream)
	resp, err := http.ReadResponse(br, r)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "connector_read_failed")
		return
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		copyHTTPResponse(w, resp)
		return
	}

	up := websocket.Upgrader{
		HandshakeTimeout: 15 * time.Second,
		CheckOrigin:      func(r *http.Request) bool { return true },
	}
	browser, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer browser.Close()

	errCh := make(chan error, 2)
	go bridgeBrowserToStream(browser, stream, errCh)
	go bridgeStreamToBrowser(br, browser, errCh)
	<-errCh
}

// writeHTTPRequest writes an HTTP/1.1 request onto the stream with an exact
// request line and headers, so WebSocket upgrade semantics are preserved.
func writeHTTPRequest(w io.Writer, r *http.Request, targetURI string) error {
	if r == nil {
		return errors.New("nil request")
	}
	if _, err := fmt.Fprintf(w, "%s %s HTTP/1.1\r\n", r.Method, targetURI); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Host: %s\r\n", r.Host); err != nil {
		return err
	}

	isUpgrade := websocket.IsWebSocketUpgrade(r)

	// Fold multiple Cookie headers into a single one.
	cookies := r.Cookies()
	if len(cookies) > 0 {
		parts := make([]string, 0, len(cookies))
		for _, c := range cookies {
			parts = append(parts, c.Name+"="+c.Value)
		}
		r.Header.Set("Cookie", strings.Join(parts, "; "))
	}

	for name, values := range r.Header {
		if isHopByHop(name) && !(isUpgrade && (strings.EqualFold(name, "Connection") || strings.EqualFold(name, "Upgrade"))) {
			continue
		}
		if strings.EqualFold(name, "Content-Length") || strings.EqualFold(name, "Transfer-Encoding") {
			continue
		}
		for _, v := range values {
			if _, err := fmt.Fprintf(w, "%s: %s\r\n", name, v); err != nil {
				return err
			}
		}
	}

	if r.Body != nil && r.ContentLength != 0 {
		if _, err := fmt.Fprint(w, "Transfer-Encoding: chunked\r\n\r\n"); err != nil {
			return err
		}
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				if _, werr := fmt.Fprintf(w, "%x\r\n", n); werr != nil {
					return werr
				}
				if _, werr := w.Write(buf[:n]); werr != nil {
					return werr
				}
				if _, werr := fmt.Fprint(w, "\r\n"); werr != nil {
					return werr
				}
			}
			if err != nil {
				break
			}
		}
		_, err := fmt.Fprint(w, "0\r\n\r\n")
		return err
	}
	_, err := fmt.Fprint(w, "Content-Length: 0\r\n\r\n")
	return err
}

// copyHTTPResponse copies a relayed HTTP response to the client, dropping
// hop-by-hop headers so the Go HTTP server can manage framing.
func copyHTTPResponse(w http.ResponseWriter, resp *http.Response) {
	for name, values := range resp.Header {
		if isHopByHop(name) {
			continue
		}
		if strings.EqualFold(name, "Content-Length") {
			continue
		}
		for _, v := range values {
			w.Header().Add(name, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if resp.Body != nil {
		_, _ = io.Copy(w, resp.Body)
	}
}

func writeJSONError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`, code)
}

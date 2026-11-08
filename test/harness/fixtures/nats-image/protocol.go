package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// A single synchronous connection owns every data frame and its reply. No
// library publish, automatic retry, reconnect, or background writer is used.
type wire struct {
	conn   net.Conn
	reader *bufio.Reader
	ctx    context.Context
	frames int
	info   map[string]any
	inbox  string
}

type auditedConn struct {
	net.Conn
	input, output *boundedLog
	closed        bool
}

func (c *auditedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		_, logErr := c.input.Write(p[:n])
		err = errors.Join(err, logErr)
	}
	return n, err
}
func (c *auditedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		_, logErr := c.output.Write(p[:n])
		err = errors.Join(err, logErr)
	}
	return n, err
}
func (c *auditedConn) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	return errors.Join(c.Conn.Close(), c.input.file.Sync(), c.output.file.Sync(), c.input.file.Close(), c.output.file.Close())
}
func bindInfo(info map[string]any, process *processRecord, address string) error {
	var config map[string]any
	if err := strictJSON(process.Config, &config); err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	proto, protoOK := info["proto"].(json.Number)
	nativePort, portOK := info["port"].(json.Number)
	payload, payloadOK := info["max_payload"].(json.Number)
	configuredPayload, configOK := config["max_payload"].(json.Number)
	if config["listen"] != address || config["server_name"] != process.Name || info["server_name"] != process.Name ||
		info["host"] != host || !protoOK || proto.String() != "1" || !portOK || nativePort.String() != port ||
		!payloadOK || !configOK || payload.String() != configuredPayload.String() ||
		(process.InfoID != "" && info["server_id"] != process.InfoID) {
		return errors.New("native INFO disagrees with the selected process configuration")
	}
	return nil
}

func inboxPrefix(user string, connection int) (string, error) {
	if (user != "manager" && user != "publisher" && user != "reader") || connection < 0 || connection >= 64 {
		return "", errors.New("unsupported connection scope")
	}
	return fmt.Sprintf("_INBOX.IMAGE.%s.C%03d", user, connection), nil
}

func connect(ctx context.Context, address, user, logBase string, connection int) (*wire, error) {
	inbox, err := inboxPrefix(user, connection)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: time.Second}
	native, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	input, err := os.OpenFile(logBase+".received", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		native.Close()
		return nil, err
	}
	output, err := os.OpenFile(logBase+".sent", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		input.Close()
		native.Close()
		return nil, err
	}
	conn := &auditedConn{Conn: native, input: &boundedLog{file: input}, output: &boundedLog{file: output}}
	w := &wire{conn: conn, reader: bufio.NewReaderSize(conn, 16384), ctx: ctx, inbox: inbox}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	if err = w.deadline(); err != nil {
		return nil, err
	}
	line, err := w.line()
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(line, []byte("INFO ")) {
		return nil, errors.New("native INFO missing")
	}
	if err = strictJSON(bytes.TrimSpace(line[5:]), &w.info); err != nil {
		return nil, err
	}
	options, err := json.Marshal(map[string]any{"user": user, "pass": "fixture-only", "verbose": false, "pedantic": true, "protocol": 1})
	if err != nil {
		return nil, err
	}
	for _, frame := range [][]byte{append(append([]byte("CONNECT "), options...), '\r', '\n'), []byte("SUB " + inbox + ".> 1\r\n"), []byte("PING\r\n")} {
		if err = w.send(frame); err != nil {
			return nil, err
		}
	}
	// Pinned protocol 1 queues PONG followed by one CONNECT INFO in
	// server/client.go. Finish that exact handshake before any API request.
	line, err = w.line()
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(line, []byte("PONG\r\n")) {
		return nil, fmt.Errorf("unexpected handshake response: %q", line)
	}
	line, err = w.line()
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(line, []byte("INFO ")) {
		return nil, errors.New("post-CONNECT native INFO missing")
	}
	var connected map[string]any
	if err = strictJSON(bytes.TrimSpace(line[5:]), &connected); err != nil {
		return nil, err
	}
	if err = connectInfo(w.info, connected); err != nil {
		return nil, err
	}
	ok = true
	return w, nil
}

func connectInfo(initial, connected map[string]any) error {
	if connected["connect_info"] != true || connected["remote_account"] != "APP" {
		return errors.New("native post-CONNECT account binding changed")
	}
	if system, present := connected["acc_is_sys"]; present && system != false {
		return errors.New("native client unexpectedly bound to the system account")
	}
	if _, present := initial["connect_info"]; present {
		return errors.New("initial INFO is a CONNECT response")
	}
	for _, field := range []string{"server_id", "server_name", "version", "git_commit", "go", "host", "client_ip", "xkey"} {
		text, ok := initial[field].(string)
		if !ok || text == "" || !reflect.DeepEqual(initial[field], connected[field]) {
			return fmt.Errorf("native INFO identity changed: %s", field)
		}
	}
	for _, field := range []string{"proto", "port", "max_payload", "client_id", "api_lvl"} {
		value, ok := number(initial[field])
		if !ok || value < 1 || !reflect.DeepEqual(initial[field], connected[field]) {
			return fmt.Errorf("native INFO numeric identity changed: %s", field)
		}
	}
	for _, field := range []string{"headers", "auth_required", "jetstream"} {
		if initial[field] != true || connected[field] != true {
			return fmt.Errorf("native INFO capability changed: %s", field)
		}
	}
	return nil
}

func (w *wire) deadline() error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	end := time.Now().Add(10 * time.Second)
	if outer, ok := w.ctx.Deadline(); ok && outer.Before(end) {
		end = outer
	}
	return w.conn.SetDeadline(end)
}

func (w *wire) send(frame []byte) error {
	if len(frame) > 16384 {
		return errors.New("wire frame exceeds bound")
	}
	if err := w.deadline(); err != nil {
		return err
	}
	for remaining := frame; len(remaining) > 0; {
		n, err := w.conn.Write(remaining)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		remaining = remaining[n:]
	}
	w.frames++
	return nil
}

func (w *wire) line() ([]byte, error) {
	if err := w.deadline(); err != nil {
		return nil, err
	}
	line, err := w.reader.ReadSlice('\n')
	if err != nil {
		return nil, err
	}
	if !bytes.HasSuffix(line, []byte("\r\n")) {
		return nil, errors.New("unterminated native protocol line")
	}
	return append([]byte(nil), line...), nil
}

func publication(subject, reply string, body []byte) []byte {
	header := []byte(fmt.Sprintf("PUB %s %s %d\r\n", subject, reply, len(body)))
	return append(append(header, body...), '\r', '\n')
}

func (w *wire) response(reply string) ([]byte, []byte, error) {
	for {
		line, err := w.line()
		if err != nil {
			return nil, nil, err
		}
		if bytes.Equal(line, []byte("PING\r\n")) {
			if err = w.send([]byte("PONG\r\n")); err != nil {
				return nil, nil, err
			}
			continue
		}
		parts := strings.Split(strings.TrimSuffix(string(line), "\r\n"), " ")
		if len(parts) != 4 || parts[0] != "MSG" || parts[1] != reply || parts[2] != "1" {
			return nil, nil, fmt.Errorf("uncorrelated native response: %q", line)
		}
		size, err := strconv.Atoi(parts[3])
		if err != nil || size < 0 || size > 16384 || strconv.Itoa(size) != parts[3] {
			return nil, nil, errors.New("invalid native response size")
		}
		body := make([]byte, size+2)
		if _, err = io.ReadFull(w.reader, body); err != nil {
			return nil, nil, err
		}
		if !bytes.HasSuffix(body, []byte("\r\n")) {
			return nil, nil, errors.New("native response missing body terminator")
		}
		return append(line, body...), body[:size], nil
	}
}

func strictJSON(data []byte, value any) error {
	// Preserve exact-number and duplicate-key boundaries even for native replies.
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var read func() error
	read = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					key, err := d.Token()
					if err != nil {
						return err
					}
					name, ok := key.(string)
					if !ok || seen[name] {
						return errors.New("duplicate or invalid JSON key")
					}
					seen[name] = true
					if err = read(); err != nil {
						return err
					}
				}
				end, err := d.Token()
				if err != nil || end != json.Delim('}') {
					return errors.New("invalid JSON object")
				}
			case '[':
				for d.More() {
					if err := read(); err != nil {
						return err
					}
				}
				end, err := d.Token()
				if err != nil || end != json.Delim(']') {
					return errors.New("invalid JSON array")
				}
			default:
				return errors.New("unexpected JSON delimiter")
			}
		}
		return nil
	}
	if err := read(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(value)
}

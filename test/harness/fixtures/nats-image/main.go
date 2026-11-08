// This tool coordinates unchanged native processes; it contains no NATS server
// implementation. Its output must pass the separate Python validators.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

type plan struct {
	Schema          int      `json:"schema_version"`
	Messages        int      `json:"messages"`
	PayloadBytes    int      `json:"payload_bytes"`
	MeasuredSeconds int      `json:"measured_seconds"`
	Topologies      []string `json:"topologies"`
}
type input struct {
	Schema   int                 `json:"schema_version"`
	Topology string              `json:"topology"`
	Plan     plan                `json:"plan"`
	Configs  [][]json.RawMessage `json:"configs"`
	Seconds  int                 `json:"seconds"`
}
type attempt struct {
	Ordinal      int    `json:"ordinal"`
	Logical      int    `json:"logical_message"`
	Connection   string `json:"connection"`
	FrameOrdinal int    `json:"frame_ordinal"`
	Request      []byte `json:"request"`
	Response     []byte `json:"response"`
	Forwarded    int64  `json:"forwarded_order"`
	Responded    int64  `json:"response_order"`
	Applied      int64  `json:"application_order"`
	Result       string `json:"application_result"`
	Outcome      string `json:"server_outcome"`
	Retry        *int   `json:"retry_of"`
}
type census struct {
	Connection string    `json:"connection"`
	Inbox      string    `json:"inbox"`
	Closed     int64     `json:"closed_order"`
	Attempts   []attempt `json:"attempts"`
}
type basic struct {
	Schema     int              `json:"schema_version"`
	Kind       string           `json:"kind"`
	Plan       plan             `json:"plan"`
	Topology   string           `json:"topology"`
	Census     census           `json:"census"`
	Boundaries map[string]int64 `json:"boundaries"`
	Processes  []*processRecord `json:"processes"`
}
type rpcRecord struct {
	Name     string `json:"name"`
	Request  []byte `json:"request"`
	Response []byte `json:"response"`
	Body     []byte `json:"body"`
	Order    int64  `json:"order"`
}
type exported struct {
	Sequence int    `json:"sequence"`
	Order    int64  `json:"order"`
	Response []byte `json:"response"`
}
type report struct {
	Schema           int             `json:"schema_version"`
	Kind             string          `json:"kind"`
	Status           string          `json:"status"`
	Error            string          `json:"error"`
	Basic            basic           `json:"basic"`
	RPC              []rpcRecord     `json:"rpc"`
	InitialInventory []observation   `json:"initial_inventory"`
	Initial          map[string]any  `json:"initial"`
	Sealed           map[string]any  `json:"sealed"`
	Group            *string         `json:"stream_group"`
	Before           collection      `json:"before"`
	After            collection      `json:"after"`
	Exports          []exported      `json:"exports"`
	Denial           []byte          `json:"restart_publication_denial"`
	Cleanup          bool            `json:"cleanup_complete"`
	StoreSnapshots   []storeSnapshot `json:"store_snapshots"`
}

func payload(logical, size int) []byte {
	result := []byte{0, 13, 10, 255, 128, byte(logical >> 24), byte(logical >> 16), byte(logical >> 8), byte(logical)}
	for j := 0; len(result) < size; j++ {
		result = append(result, byte((37*j+17*logical)%256))
	}
	return result
}
func (r *runtimeState) open(index int, user string) (*wire, error) {
	if len(r.wires) >= 64 {
		return nil, errors.New("connection count exceeded")
	}
	address := fmt.Sprintf("127.0.0.1:%d", 19001+index)
	var selected *nativeChild
	for i := len(r.children) - 1; i >= 0; i-- {
		if r.children[i].record.Name == fmt.Sprintf("image-S-%d", index+1) {
			selected = r.children[i]
			break
		}
	}
	if selected == nil {
		return nil, errors.New("connection has no selected native process")
	}
	select {
	case <-selected.done:
		return nil, errors.New("selected native process already exited")
	default:
	}
	w, err := connect(r.ctx, address, user, filepath.Join(r.dir, "wire", fmt.Sprintf("%03d-%s", len(r.wires), user)), len(r.wires))
	if err != nil {
		return nil, err
	}
	if err = bindInfo(w.info, selected.record, address); err != nil {
		w.conn.Close()
		return nil, err
	}
	r.wires = append(r.wires, w)
	return w, nil
}
func (r *runtimeState) ready(count, generation int) error {
	end := time.Now().Add(60 * time.Second)
	for {
		if err := r.alive(generation); err != nil {
			return err
		}
		healthy := true
		for i := 0; i < count; i++ {
			value, err := r.observe(i, "/healthz")
			if err != nil {
				if r.ctx.Err() != nil {
					return r.ctx.Err()
				}
				if r.fatal != nil {
					return r.fatal
				}
				healthy = false
				continue
			}
			parsed, err := object(value.Body)
			if err != nil || value.Status != 200 || parsed["status"] != "ok" {
				healthy = false
			}
		}
		if healthy {
			break
		}
		if time.Now().After(end) {
			return errors.New("native readiness deadline exceeded")
		}
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	for i := 0; i < count; i++ {
		w, err := r.open(i, "reader")
		if err != nil {
			return err
		}
		id, ok := w.info["server_id"].(string)
		if !ok || id == "" {
			w.conn.Close()
			return errors.New("native INFO ID missing")
		}
		r.children[generation*count+i].record.InfoID = id
		if err = w.conn.Close(); err != nil {
			return err
		}
	}
	return nil
}
func (r *runtimeState) rpc(w *wire, output *report, name, subject string, body []byte) (map[string]any, error) {
	reply := w.inbox + ".R" + strconv.Itoa(len(output.RPC)+1)
	row := rpcRecord{Name: name, Request: publication(subject, reply, body)}
	// Durable intended request precedes native dispatch; wire files retain the
	// actual available bytes if writing or reading later fails.
	if err := ownedWrite(filepath.Join(r.dir, "rpc", fmt.Sprintf("%03d.request", len(output.RPC)+1)), row.Request); err != nil {
		return nil, err
	}
	if err := w.send(row.Request); err != nil {
		return nil, err
	}
	var err error
	row.Response, row.Body, err = w.response(reply)
	row.Order = r.order.Add(1)
	output.RPC = append(output.RPC, row)
	if err != nil {
		return nil, err
	}
	if err = ownedWrite(filepath.Join(r.dir, "rpc", fmt.Sprintf("%03d.response", len(output.RPC))), row.Response); err != nil {
		return nil, err
	}
	value, err := object(row.Body)
	if err != nil {
		return nil, err
	}
	if nativeError, present := value["error"]; present {
		return nil, fmt.Errorf("native API rejected %s: %v", name, nativeError)
	}
	return value, nil
}
func (r *runtimeState) publications(output *report, p plan) error {
	w, err := r.open(0, "publisher")
	if err != nil {
		return err
	}
	logical := []int{0, 1, 1}
	for i := 2; i < p.Messages; i++ {
		logical = append(logical, i)
	}
	output.Basic.Census.Connection = "publisher-1"
	output.Basic.Census.Inbox = w.inbox
	for index, message := range logical {
		ordinal := index + 1
		reply := fmt.Sprintf("%s.P%d", w.inbox, ordinal)
		row := attempt{Ordinal: ordinal, Logical: message, Connection: "publisher-1", Request: publication("fence.input", reply, payload(message, p.PayloadBytes)), Result: "ACK", Outcome: "UNKNOWN"}
		if ordinal == 3 {
			previous := 2
			row.Retry = &previous
		}
		if err = ownedWrite(filepath.Join(r.dir, "attempts", fmt.Sprintf("%03d.request", ordinal)), row.Request); err != nil {
			return err
		}
		if err = w.send(row.Request); err != nil {
			return err
		}
		row.Forwarded = r.order.Add(1)
		row.FrameOrdinal = w.frames
		var body []byte
		row.Response, body, err = w.response(reply)
		row.Responded = r.order.Add(1)
		if err != nil {
			output.Basic.Census.Attempts = append(output.Basic.Census.Attempts, row)
			return err
		}
		if err = ownedWrite(filepath.Join(r.dir, "attempts", fmt.Sprintf("%03d.response", ordinal)), row.Response); err != nil {
			return err
		}
		ack, err := object(body)
		if err != nil {
			return err
		}
		seq, ok := number(ack["seq"])
		if len(ack) != 2 || ack["stream"] != "FENCE" || !ok || seq != int64(ordinal) {
			return errors.New("unexpected native publication acknowledgment")
		}
		row.Outcome = "ACKNOWLEDGED"
		if ordinal == 2 {
			// The real server response is already retained. Deliberately do
			// not deliver it to the application: its own wait actually expires.
			neverDelivered := make(chan struct{})
			timer := time.NewTimer(25 * time.Millisecond)
			select {
			case <-neverDelivered:
				timer.Stop()
				return errors.New("suppressed ACK was delivered")
			case <-r.ctx.Done():
				timer.Stop()
				return r.ctx.Err()
			case <-timer.C:
			}
			row.Result = "TIMEOUT"
		}
		row.Applied = r.order.Add(1)
		output.Basic.Census.Attempts = append(output.Basic.Census.Attempts, row)
	}
	if err = w.conn.Close(); err != nil {
		return err
	}
	output.Basic.Census.Closed = r.order.Add(1)
	output.Basic.Boundaries["publishers_closed"] = output.Basic.Census.Closed
	return nil
}

func execute(r *runtimeState, in input, out *report) (err error) {
	count := 1
	if in.Topology == "R3" {
		count = 3
	}
	if err = verifyNative(); err != nil {
		return err
	}
	for i := 0; i < count; i++ {
		if _, err = r.start(0, i, in.Configs[0][i]); err != nil {
			return err
		}
	}
	if err = r.ready(count, 0); err != nil {
		return err
	}
	manager, err := r.open(0, "manager")
	if err != nil {
		return err
	}
	config := map[string]any{"name": "FENCE", "subjects": []string{"fence.input"}, "retention": "limits", "storage": "file", "num_replicas": count,
		"max_msgs": in.Plan.Messages + 17, "max_bytes": 8388608, "max_age": 0, "discard": "new", "deny_delete": true, "deny_purge": true, "allow_rollup_hdrs": false, "sealed": false}
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	if out.Initial, err = r.rpc(manager, out, "create", "$JS.API.STREAM.CREATE.FENCE", data); err != nil {
		return err
	}
	for i := 0; i < count; i++ {
		observed, e := r.observe(i, jszPath)
		out.InitialInventory = append(out.InitialInventory, observed)
		if e != nil {
			return e
		}
		stream, e := nativeStream(observed.Body)
		if e != nil {
			return e
		}
		if count == 3 {
			group, ok := stream["stream_raft_group"].(string)
			if !ok || group == "" {
				return errors.New("initial stream Raft group missing")
			}
			if out.Group != nil && *out.Group != group {
				return errors.New("initial stream group differs across voters")
			}
			out.Group = &group
		}
	}
	if err = r.publications(out, in.Plan); err != nil {
		return err
	}
	nativeConfig, ok := out.Initial["config"].(map[string]any)
	if !ok {
		return errors.New("native create configuration missing")
	}
	sealedConfig := map[string]any{}
	for key, value := range nativeConfig {
		sealedConfig[key] = value
	}
	sealedConfig["sealed"] = true
	data, err = json.Marshal(sealedConfig)
	if err != nil {
		return err
	}
	if out.Sealed, err = r.rpc(manager, out, "seal", "$JS.API.STREAM.UPDATE.FENCE", data); err != nil {
		return err
	}
	out.Basic.Boundaries["seal_ack"] = r.order.Add(1)
	for _, w := range r.wires {
		if err = w.conn.Close(); err != nil {
			return err
		}
	}
	if err = r.stopAll(0); err != nil {
		return err
	}
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("image-S-%d", i+1)
		snapshot := storeSnapshot{Name: name, Started: r.order.Add(1)}
		snapshot.Files, err = snapshotStore(r.ctx, filepath.Join(r.dir, "stores", name), filepath.Join(r.dir, "before-stores", name))
		snapshot.Finished = r.order.Add(1)
		out.StoreSnapshots = append(out.StoreSnapshots, snapshot)
		if err != nil {
			return err
		}
	}
	out.Basic.Boundaries["restart_started"] = r.order.Add(1)
	for i := 0; i < count; i++ {
		if _, err = r.start(1, i, in.Configs[1][i]); err != nil {
			return err
		}
	}
	if err = r.ready(count, 1); err != nil {
		return err
	}
	denied, err := r.open(0, "reader")
	if err != nil {
		return err
	}
	if err = denied.send(publication("fence.input", denied.inbox+".DENIED", []byte("forbidden"))); err != nil {
		return err
	}
	if out.Denial, err = denied.line(); err != nil {
		return err
	}
	if !bytes.Equal(out.Denial, []byte("-ERR 'Permissions Violation for Publish to \"fence.input\"'\r\n")) {
		return fmt.Errorf("restart ACL did not reject data publish: %q", out.Denial)
	}
	if err = denied.conn.Close(); err != nil {
		return err
	}
	group := ""
	if out.Group != nil {
		group = *out.Group
	}
	if out.Before, err = r.settledCollection(count, group); err != nil {
		return err
	}
	out.Basic.Boundaries["fence_checked"] = r.order.Add(1)
	reader, err := r.open(0, "reader")
	if err != nil {
		return err
	}
	info, err := r.rpc(reader, out, "fenced-info", "$JS.API.STREAM.INFO.FENCE", []byte("{}"))
	if err != nil {
		return err
	}
	state, ok := nested(info, "state")
	if !ok {
		return errors.New("fenced stream state missing")
	}
	upper, ok := number(state["last_seq"])
	if !ok || upper != int64(in.Plan.Messages+1) {
		return errors.New("unexpected fenced interval")
	}
	for seq := 1; seq <= int(upper); seq++ {
		request := []byte(fmt.Sprintf("{\"seq\":%d}", seq))
		if _, err = r.rpc(reader, out, fmt.Sprintf("export-%d", seq), "$JS.API.STREAM.MSG.GET.FENCE", request); err != nil {
			return err
		}
		rpc := out.RPC[len(out.RPC)-1]
		out.Exports = append(out.Exports, exported{Sequence: seq, Order: rpc.Order, Response: rpc.Body})
	}
	out.Basic.Boundaries["export_complete"] = r.order.Add(1)
	if out.After, err = r.settledCollection(count, group); err != nil {
		return err
	}
	if err = reader.conn.Close(); err != nil {
		return err
	}
	return nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: driver /proof/input.json")
		os.Exit(2)
	}
	data, err := regularBytes(os.Args[1], 1<<20)
	var in input
	if err == nil {
		err = strictJSON(data, &in)
	}
	if err == nil {
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		err = d.Decode(&in)
	}
	count := 1
	if in.Topology == "R3" {
		count = 3
	}
	if err == nil && (in.Schema != 1 || (in.Topology != "standalone" && in.Topology != "R3") || in.Seconds < 10 || in.Seconds > 1800 ||
		in.Plan.Messages < 2 || in.Plan.Messages > 256 || in.Plan.PayloadBytes < 16 || in.Plan.PayloadBytes > 4096 || len(in.Configs) != 2 || len(in.Configs[0]) != count || len(in.Configs[1]) != count) {
		err = errors.New("invalid bounded driver input")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	signalCtx, signalCancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer signalCancel()
	ctx, cancel := context.WithTimeout(signalCtx, time.Duration(in.Seconds)*time.Second)
	defer cancel()
	r := &runtimeState{ctx: ctx, dir: "/proof"}
	out := report{Schema: 1, Kind: "nats-unchanged-image-native-observations", Status: "FAIL", Basic: basic{Schema: 1, Kind: "nats-image-supplied-observations", Plan: in.Plan, Topology: in.Topology, Boundaries: map[string]int64{}}}
	for _, name := range []string{"config", "stores", "logs", "wire", "rpc", "attempts", "http", "before-stores"} {
		if err = os.Mkdir(filepath.Join(r.dir, name), 0700); err != nil {
			break
		}
	}
	if err == nil {
		err = execute(r, in, &out)
	}
	for _, w := range r.wires {
		err = errors.Join(err, w.conn.Close())
	}
	cleanup := r.stopAll(-1)
	err = errors.Join(err, cleanup)
	out.Cleanup = cleanup == nil
	for _, child := range r.children {
		out.Basic.Processes = append(out.Basic.Processes, child.record)
	}
	out.Basic.Boundaries["finish"] = r.order.Add(1)
	err = errors.Join(err, ctx.Err())
	if err == nil {
		out.Status = "PASS"
	} else {
		out.Error = err.Error()
	}
	result, marshalErr := json.MarshalIndent(out, "", "  ")
	err = errors.Join(err, marshalErr)
	if marshalErr == nil {
		err = errors.Join(err, ownedWrite(filepath.Join(r.dir, "native.json"), result))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Println("Native observation collection complete; independent validation is required")
}

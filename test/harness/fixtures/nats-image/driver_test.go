package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProtocolOneConsumesAndBindsConnectInfo(t *testing.T) {
	initial := map[string]any{"server_id": "NATIVE-ID", "server_name": "image-S-1", "version": "2.15.2", "git_commit": "5a815f1",
		"go": "go1.27.3", "host": "127.0.0.1", "port": 19001, "proto": 1, "max_payload": 8192, "headers": true,
		"auth_required": true, "jetstream": true, "client_id": 7, "client_ip": "127.0.0.1", "api_lvl": 5, "xkey": "PUBLIC-KEY"}
	for _, mode := range []string{"valid", "server", "account", "system", "flag", "duplicate", "missing", "extra"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(time.Second))
				first, _ := json.Marshal(initial)
				fmt.Fprintf(conn, "INFO %s \r\n", first)
				reader := bufio.NewReader(conn)
				for index := 0; index < 3; index++ {
					if _, err = reader.ReadString('\n'); err != nil {
						return
					}
				}
				fmt.Fprint(conn, "PONG\r\n")
				connected := map[string]any{}
				for key, value := range initial {
					connected[key] = value
				}
				connected["connect_info"], connected["remote_account"] = true, "APP"
				switch mode {
				case "server":
					connected["server_id"] = "OTHER"
				case "account":
					connected["remote_account"] = "SYS"
				case "system":
					connected["acc_is_sys"] = true
				case "flag":
					connected["connect_info"] = 1
				case "missing":
					return
				case "extra":
					fmt.Fprint(conn, "PONG\r\n")
				}
				second, _ := json.Marshal(connected)
				if mode == "duplicate" {
					second = append(second[:len(second)-1], []byte(",\"remote_account\":\"APP\"}")...)
				}
				fmt.Fprintf(conn, "INFO %s \r\n", second)
				if mode == "valid" {
					fmt.Fprint(conn, "MSG _INBOX.NEXT 1 2\r\n{}\r\n")
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			w, err := connect(ctx, listener.Addr().String(), "manager", filepath.Join(t.TempDir(), "wire"), 0)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				_, body, responseErr := w.response("_INBOX.NEXT")
				if responseErr != nil || string(body) != "{}" {
					t.Errorf("connect INFO leaked into API response: %v", responseErr)
				}
			} else if err == nil {
				t.Error("accepted invalid or missing connect INFO")
			}
			if w != nil {
				w.conn.Close()
			}
			listener.Close()
			<-done
		})
	}
}

func TestConnectionSubscriptionsHaveSeparateRoleAndConnectionScopes(t *testing.T) {
	for index, role := range []string{"manager", "publisher", "reader", "reader"} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		subscription := make(chan string, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(time.Second))
			info := map[string]any{"server_id": "NATIVE-ID", "server_name": "image-S-1", "version": "2.15.2", "git_commit": "5a815f1",
				"go": "go1.27.3", "host": "127.0.0.1", "port": 19001, "proto": 1, "max_payload": 8192, "headers": true,
				"auth_required": true, "jetstream": true, "client_id": 7, "client_ip": "127.0.0.1", "api_lvl": 5, "xkey": "PUBLIC-KEY"}
			data, _ := json.Marshal(info)
			fmt.Fprintf(conn, "INFO %s\r\n", data)
			reader := bufio.NewReader(conn)
			for i := 0; i < 3; i++ {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if i == 1 {
					subscription <- line
				}
			}
			info["connect_info"], info["remote_account"] = true, "APP"
			data, _ = json.Marshal(info)
			fmt.Fprintf(conn, "PONG\r\nINFO %s\r\n", data)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		w, err := connect(ctx, listener.Addr().String(), role, filepath.Join(t.TempDir(), "wire"), index)
		if err != nil {
			t.Fatal(err)
		}
		w.conn.Close()
		cancel()
		listener.Close()
		<-done
		select {
		case actual := <-subscription:
			want := fmt.Sprintf("SUB _INBOX.IMAGE.%s.C%03d.> 1\r\n", role, index)
			if actual != want {
				t.Errorf("role %s connection %d subscribed to %q; want %q", role, index, actual, want)
			}
		default:
			t.Fatal("subscription not observed")
		}
	}
}

func TestInitialInventoryWaitsForEveryReplica(t *testing.T) {
	processes := []*processRecord{{Name: "image-S-1", InfoID: "A"}, {Name: "image-S-2", InfoID: "B"}, {Name: "image-S-3", InfoID: "C"}}
	initial := map[string]any{"created": "2026-11-09T00:00:00Z", "config": map[string]any{"name": "FENCE", "metadata": map[string]any{"_nats.req.level": "0", "_nats.level": "5", "_nats.ver": "2.15.2"}}}
	makeObservation := func(index int, ready bool) observation {
		account := map[string]any{"name": "APP", "id": "APP"}
		streams := 0
		if ready {
			streams = 1
			account["stream_detail"] = []any{map[string]any{"name": "FENCE", "created": initial["created"], "config": map[string]any{"name": "FENCE", "metadata": map[string]any{"_nats.req.level": "0"}}, "stream_raft_group": "S-R3F-known", "state": map[string]any{"messages": 0, "bytes": 0, "first_seq": 0, "last_seq": 0, "consumer_count": 0}}}
		}
		body, _ := json.Marshal(map[string]any{"server_id": processes[index].InfoID, "total": 1, "streams": streams, "consumers": 0, "messages": 0, "bytes": 0, "account_details": []any{account}})
		return observation{Node: processes[index].Name, Path: jszPath, Status: 200, Body: body}
	}
	calls := 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rows, group, err := waitInitial(ctx, processes, initial, func(_ context.Context, index int) (observation, error) {
		calls++
		v := makeObservation(index, index != 2 || calls > 3)
		v.Order = int64(calls)
		return v, nil
	})
	if err != nil || calls != 6 || len(rows) != 3 || rows[0].Order != 4 || group == nil || *group != "S-R3F-known" {
		t.Fatalf("incomplete collection admitted or valid delay failed: %d %v %v", calls, group, err)
	}
	for _, mode := range []string{"status", "foreign", "drift", "type", "group", "malformed", "nonempty-pending"} {
		calls = 0
		_, _, err = waitInitial(ctx, processes, initial, func(_ context.Context, index int) (observation, error) {
			calls++
			v := makeObservation(index, true)
			switch mode {
			case "status":
				v.Status = 503
			case "foreign":
				v.Body = bytes.ReplaceAll(v.Body, []byte(`"server_id":"A"`), []byte(`"server_id":"foreign"`))
			case "drift":
				v.Body = bytes.ReplaceAll(v.Body, []byte(`"_nats.req.level":"0"`), []byte(`"_nats.req.level":"1"`))
			case "type":
				v.Body = bytes.ReplaceAll(v.Body, []byte(`"streams":1`), []byte(`"streams":true`))
			case "group":
				v.Body = bytes.ReplaceAll(v.Body, []byte(`"S-R3F-known"`), []byte(`"foreign"`))
			case "malformed":
				v.Body = []byte(`{`)
			case "nonempty-pending":
				v = makeObservation(index, false)
				v.Body = bytes.ReplaceAll(v.Body, []byte(`"messages":0`), []byte(`"messages":1`))
			}
			return v, nil
		})
		if err == nil || calls != 1 {
			t.Errorf("%s retried or admitted invalid evidence: calls=%d err=%v", mode, calls, err)
		}
	}
	short, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	calls = 0
	_, _, err = waitInitial(short, processes, initial, func(_ context.Context, index int) (observation, error) {
		calls++
		return makeObservation(index, false), nil
	})
	if err == nil || calls != 3 {
		t.Fatalf("pending inventory exceeded its shared deadline: %d %v", calls, err)
	}
	_, _, err = waitInitial(short, processes, initial, func(_ context.Context, index int) (observation, error) {
		t.Error("dispatch after deadline")
		return makeObservation(index, true), nil
	})
	if err == nil {
		t.Fatal("expired parent budget accepted")
	}
}

func TestInitialMaterializationRejectsEveryChangedField(t *testing.T) {
	process := &processRecord{Name: "image-S-1", InfoID: "A"}
	initial := map[string]any{"created": "2026-11-09T00:00:00Z", "config": map[string]any{"name": "FENCE", "max_msgs": 21, "metadata": map[string]any{"_nats.req.level": "0", "_nats.level": "5", "_nats.ver": "2.15.2"}}}
	build := func(ready, standalone bool, mutate func(body, account, stream, state map[string]any)) observation {
		state := map[string]any{"messages": 0, "bytes": 0, "first_seq": 0, "last_seq": 0, "consumer_count": 0}
		stream := map[string]any{"name": "FENCE", "created": initial["created"], "config": map[string]any{"name": "FENCE", "max_msgs": 21, "metadata": map[string]any{"_nats.req.level": "0"}}, "state": state}
		if !standalone {
			stream["stream_raft_group"] = "S-R3F-known"
		}
		account := map[string]any{"name": "APP", "id": "APP"}
		streams := 0
		if ready {
			streams = 1
			account["stream_detail"] = []any{stream}
		}
		body := map[string]any{"server_id": "A", "total": 1, "streams": streams, "consumers": 0, "messages": 0, "bytes": 0, "account_details": []any{account}}
		if mutate != nil {
			mutate(body, account, stream, state)
		}
		data, _ := json.Marshal(body)
		return observation{Node: process.Name, Path: jszPath, Status: 200, Body: data}
	}
	for _, standalone := range []bool{false, true} {
		count := 3
		if standalone {
			count = 1
		}
		if _, ready, err := initialMaterialization(build(true, standalone, nil), process, initial, count); err != nil || !ready {
			t.Fatalf("valid ready inventory rejected (standalone=%v): %v", standalone, err)
		}
		if _, ready, err := initialMaterialization(build(false, standalone, nil), process, initial, count); err != nil || ready {
			t.Fatalf("valid pending inventory rejected (standalone=%v): %v", standalone, err)
		}
	}
	type change struct {
		name  string
		ready bool
		apply func(body, account, stream, state map[string]any)
	}
	changes := []change{
		{"node", true, nil},
		{"streams range", true, func(b, _, _, _ map[string]any) { b["streams"] = 2 }},
		{"negative streams", false, func(b, _, _, _ map[string]any) { b["streams"] = -1 }},
		{"total", true, func(b, _, _, _ map[string]any) { b["total"] = 2 }},
		{"consumers", true, func(b, _, _, _ map[string]any) { b["consumers"] = 1 }},
		{"bytes", false, func(b, _, _, _ map[string]any) { b["bytes"] = 1 }},
		{"account id", true, func(_, a, _, _ map[string]any) { a["id"] = "SYS" }},
		{"account name", false, func(_, a, _, _ map[string]any) { a["name"] = "SYS" }},
		{"extra account", true, func(b, a, _, _ map[string]any) { b["account_details"] = []any{a, a} }},
		{"pending detail", false, func(_, a, s, _ map[string]any) { a["stream_detail"] = []any{s} }},
		{"null pending detail", false, func(_, a, _, _ map[string]any) { a["stream_detail"] = nil }},
		{"missing ready detail", true, func(_, a, _, _ map[string]any) { delete(a, "stream_detail") }},
		{"stream name", true, func(_, _, s, _ map[string]any) { s["name"] = "OTHER" }},
		{"created", true, func(_, _, s, _ map[string]any) { s["created"] = "2026-11-09T00:00:01Z" }},
		{"config bytes", true, func(_, _, s, _ map[string]any) { s["config"].(map[string]any)["max_msgs"] = 22 }},
		{"config type", true, func(_, _, s, _ map[string]any) { s["config"].(map[string]any)["max_msgs"] = "21" }},
		{"extra metadata key", true, func(_, _, s, _ map[string]any) {
			s["config"].(map[string]any)["metadata"] = map[string]any{"_nats.req.level": "0", "_nats.ver": "2.15.2"}
		}},
		{"missing state", true, func(_, _, s, _ map[string]any) { delete(s, "state") }},
		{"nonempty state", true, func(_, _, _, st map[string]any) { st["last_seq"] = 1 }},
		{"state type", true, func(_, _, _, st map[string]any) { st["consumer_count"] = false }},
	}
	for _, standalone := range []bool{false, true} {
		count := 3
		if standalone {
			count = 1
		}
		for _, c := range changes {
			v := build(c.ready, standalone, c.apply)
			if c.name == "node" {
				v.Node = "image-S-2"
			}
			if _, _, err := initialMaterialization(v, process, initial, count); err == nil {
				t.Errorf("%s admitted (standalone=%v)", c.name, standalone)
			}
		}
	}
	grouped := build(true, false, nil)
	if _, _, err := initialMaterialization(grouped, process, initial, 1); err == nil {
		t.Error("standalone stream Raft group admitted")
	}
	if _, _, err := initialMaterialization(build(true, true, nil), process, initial, 3); err == nil {
		t.Error("missing R3 stream Raft group admitted")
	}
	processes := []*processRecord{{Name: "image-S-1", InfoID: "A"}, {Name: "image-S-2", InfoID: "B"}, {Name: "image-S-3", InfoID: "C"}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	_, _, err := waitInitial(ctx, processes, initial, func(_ context.Context, index int) (observation, error) {
		calls++
		v := build(true, false, func(b, _, s, _ map[string]any) {
			b["server_id"] = processes[index].InfoID
			if index == 2 {
				s["stream_raft_group"] = "S-R3F-other"
			}
		})
		v.Node = processes[index].Name
		return v, nil
	})
	if err == nil || calls != 3 {
		t.Errorf("stream group changed across voters was admitted: calls=%d err=%v", calls, err)
	}
}

func TestSettledCollectionRequiresOneAgreedRaftLeader(t *testing.T) {
	names := []string{"image-S-1", "image-S-2", "image-S-3"}
	group := "S-R3F-known"
	build := func(mutate func(name, key string, row map[string]any)) collection {
		result := collection{Nodes: map[string]nodeCollection{}}
		leader := nativeMember("image-S-2")
		for _, name := range names {
			stream := map[string]any{"name": "FENCE", "config": map[string]any{"sealed": true}}
			jsz, _ := json.Marshal(map[string]any{"account_details": []any{map[string]any{"name": "APP", "id": "APP", "stream_detail": []any{stream}}}})
			node := nodeCollection{Health: observation{Status: 200, Body: []byte(`{"status":"ok"}`)}, JSZ: observation{Status: 200, Body: jsz}, Groups: map[string]observation{}}
			for key, path := range map[string][2]string{"SYS/_meta_": {"SYS", "_meta_"}, "APP/" + group: {"APP", group}} {
				state := "FOLLOWER"
				if name == "image-S-2" {
					state = "LEADER"
				}
				row := map[string]any{"id": nativeMember(name), "leader": leader, "state": state, "term": 2, "pterm": 2, "pindex": 6, "committed": 6, "applied": 6,
					"ipq_proposal_len": 0, "ipq_entry_len": 0, "ipq_resp_len": 0, "ipq_apply_len": 0}
				if mutate != nil {
					mutate(name, key, row)
				}
				body, _ := json.Marshal(map[string]any{path[0]: map[string]any{path[1]: row}})
				node.Groups[key] = observation{Status: 200, Body: body}
			}
			result.Nodes[name] = node
		}
		return result
	}
	if !settled(build(nil), 3, group) {
		t.Fatal("agreed leader rejected")
	}
	stream := "APP/" + group
	for name, mutate := range map[string]func(string, string, map[string]any){
		"no stream leader": func(_, key string, row map[string]any) {
			if key == stream {
				row["leader"] = nil
				row["state"] = "FOLLOWER"
			}
		},
		"empty meta leader": func(_, key string, row map[string]any) {
			if key == "SYS/_meta_" {
				row["leader"] = ""
				row["state"] = "FOLLOWER"
			}
		},
		"leader without leader role": func(n, key string, row map[string]any) {
			if key == stream && n == "image-S-2" {
				row["state"] = "FOLLOWER"
			}
		},
		"follower claims leader role": func(n, key string, row map[string]any) {
			if key == stream && n == "image-S-3" {
				row["state"] = "LEADER"
			}
		},
		"unknown leader": func(_, key string, row map[string]any) {
			if key == stream {
				row["leader"] = "Foreign1"
				row["state"] = "FOLLOWER"
			}
		},
		"leader disagreement": func(n, key string, row map[string]any) {
			if key == stream && n == "image-S-3" {
				row["leader"] = nativeMember("image-S-3")
				row["state"] = "LEADER"
			}
		},
		"term disagreement": func(n, key string, row map[string]any) {
			if key == stream && n == "image-S-3" {
				row["term"] = 3
			}
		},
		"log term disagreement": func(n, key string, row map[string]any) {
			if key == "SYS/_meta_" && n == "image-S-1" {
				row["pterm"] = 1
			}
		},
		"index disagreement": func(n, key string, row map[string]any) {
			if key == stream && n == "image-S-1" {
				row["pindex"], row["committed"], row["applied"] = 7, 7, 7
			}
		},
		"uncommitted entry": func(n, key string, row map[string]any) {
			if key == stream && n == "image-S-1" {
				row["committed"] = 5
			}
		},
		"unapplied entry": func(n, key string, row map[string]any) {
			if key == stream && n == "image-S-1" {
				row["applied"] = 5
			}
		},
		"follower names other leader": func(n, key string, row map[string]any) {
			if key == stream && n == "image-S-3" {
				row["leader"] = nativeMember("image-S-1")
			}
		},
		"non-numeric index": func(_, key string, row map[string]any) {
			// Every voter agrees, so only the type check can reject it.
			if key == stream {
				row["pindex"], row["committed"], row["applied"] = "x", 0, 0
			}
		},
		"missing commit index": func(_, key string, row map[string]any) {
			// Every voter agrees, so only the type check can reject it.
			if key == stream {
				row["pindex"], row["applied"] = 0, 0
				delete(row, "committed")
			}
		},
		"other member id": func(n, key string, row map[string]any) {
			if key == stream && n == "image-S-1" {
				row["id"] = "Foreign1"
			}
		},
	} {
		if settled(build(mutate), 3, group) {
			t.Errorf("%s settled", name)
		}
	}
	for _, field := range []string{"ipq_proposal_len", "ipq_entry_len", "ipq_resp_len", "ipq_apply_len"} {
		for name, change := range map[string]func(map[string]any){
			"queued":  func(row map[string]any) { row[field] = 1 },
			"missing": func(row map[string]any) { delete(row, field) },
		} {
			if settled(build(func(n, key string, row map[string]any) {
				if key == stream && n == "image-S-1" {
					change(row)
				}
			}), 3, group) {
				t.Errorf("%s %s settled", name, field)
			}
		}
	}
	// Pinned raftz names; each flag is omitted while false or empty. A WAL
	// error is a Go error value, which encoding/json writes as an object.
	for _, flag := range []struct {
		field string
		value any
	}{{"paused", true}, {"catching_up", true}, {"observer", true}, {"overrun", true},
		{"wal_error", map[string]any{}}, {"wal_error", "disk failure"}} {
		if settled(build(func(n, key string, row map[string]any) {
			if key == stream && n == "image-S-3" {
				row[flag.field] = flag.value
			}
		}), 3, group) {
			t.Errorf("%s=%v settled", flag.field, flag.value)
		}
	}
	for field, value := range map[string]any{"paused": false, "catching_up": false, "observer": false, "overrun": false, "wal_error": ""} {
		if !settled(build(func(_, _ string, row map[string]any) { row[field] = value }), 3, group) {
			t.Errorf("explicit healthy %s=%v rejected", field, value)
		}
	}
	missing := build(nil)
	for name, node := range missing.Nodes {
		delete(node.Groups, "SYS/_meta_")
		missing.Nodes[name] = node
	}
	if settled(missing, 3, group) {
		t.Error("collection without the meta group settled")
	}
	fewer := build(nil)
	delete(fewer.Nodes, "image-S-3")
	if settled(fewer, 3, group) {
		t.Error("collection from two voters settled")
	}
	for name, change := range map[string]func(*nodeCollection){
		"unhealthy":     func(node *nodeCollection) { node.Health.Body = []byte(`{"status":"unavailable"}`) },
		"health status": func(node *nodeCollection) { node.Health.Status = 503 },
		"unsealed": func(node *nodeCollection) {
			node.JSZ.Body = bytes.ReplaceAll(node.JSZ.Body, []byte(`"sealed":true`), []byte(`"sealed":false`))
		},
		"group status": func(node *nodeCollection) { g := node.Groups[stream]; g.Status = 503; node.Groups[stream] = g },
		"jsz status":   func(node *nodeCollection) { node.JSZ.Status = 503 },
	} {
		value := build(nil)
		node := value.Nodes["image-S-1"]
		change(&node)
		value.Nodes["image-S-1"] = node
		if settled(value, 3, group) {
			t.Errorf("%s node settled", name)
		}
	}
}

func TestInitialPrechecksRejectInvalidInputsBeforeObserving(t *testing.T) {
	processes := []*processRecord{{Name: "image-S-1", InfoID: "A"}, {Name: "image-S-2", InfoID: "B"}, {Name: "image-S-3", InfoID: "C"}}
	config := map[string]any{"name": "FENCE", "metadata": map[string]any{"_nats.req.level": "0", "_nats.level": "5", "_nats.ver": "2.15.2"}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for name, input := range map[string]struct {
		processes []*processRecord
		initial   map[string]any
	}{
		"two voters":      {processes[:2], map[string]any{"created": "2026-11-09T00:00:00Z", "config": config}},
		"missing created": {processes, map[string]any{"config": config}},
		"empty created":   {processes, map[string]any{"created": "", "config": config}},
		"invalid config":  {processes, map[string]any{"created": "2026-11-09T00:00:00Z", "config": "FENCE"}},
	} {
		_, _, err := waitInitial(ctx, input.processes, input.initial, func(context.Context, int) (observation, error) {
			t.Errorf("%s observed a node", name)
			return observation{}, nil
		})
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestInfoMustMatchSelectedProcessConfiguration(t *testing.T) {
	process := &processRecord{Name: "image-S-1", InfoID: "native-id", Config: []byte(`{"server_name":"image-S-1","listen":"127.0.0.1:19001","max_payload":8192}`)}
	baseline := map[string]any{"server_name": "image-S-1", "server_id": "native-id", "host": "127.0.0.1", "port": json.Number("19001"), "proto": json.Number("1"), "max_payload": json.Number("8192")}
	if err := bindInfo(baseline, process, "127.0.0.1:19001"); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		key   string
		value any
	}{
		{"server_name", "foreign-node"}, {"server_id", "foreign-id"}, {"host", "127.0.0.2"},
		{"port", json.Number("19003")}, {"proto", json.Number("2")}, {"max_payload", json.Number("4096")},
		{"port", json.Number("19001.0")}, {"proto", true}, {"max_payload", "8192"},
	} {
		copy := make(map[string]any)
		for key, value := range baseline {
			copy[key] = value
		}
		copy[change.key] = change.value
		if bindInfo(copy, process, "127.0.0.1:19001") == nil {
			t.Errorf("accepted changed %s=%v", change.key, change.value)
		}
	}
}

func TestCapturedPublisherAckCannotSatisfyManagementReply(t *testing.T) {
	// This exact response shape was retained in failed attempt 02's manager
	// socket. The repair must isolate subscriptions, never skip this message.
	left, right := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer right.Close()
		fmt.Fprint(right, "MSG _INBOX.IMAGE.P1 1 26\r\n{\"stream\":\"FENCE\",\"seq\":1}\r\n")
	}()
	w := wire{conn: left, reader: bufio.NewReader(left), ctx: ctx}
	if _, _, err := w.response("_INBOX.IMAGE.manager.C001.R2"); err == nil {
		t.Fatal("foreign publisher acknowledgment accepted")
	}
	left.Close()
	<-done
}

func TestStrictNativeJSON(t *testing.T) {
	for _, data := range []string{`{"stream":"FENCE","seq":1,"seq":2}`, `{"x":{"a":1,"a":2}}`, `{} {}`, `[NaN]`} {
		var value any
		if strictJSON([]byte(data), &value) == nil {
			t.Fatalf("accepted invalid native JSON: %s", data)
		}
	}
	var value map[string]any
	if err := strictJSON([]byte("{\"stream\":\"FENCE\",\"seq\":1}\r\n"), &value); err != nil {
		t.Fatal(err)
	}
	if value["seq"] != json.Number("1") {
		t.Fatal("native number was coerced")
	}
}
func TestBinaryPayloadAndNativeMemberIdentity(t *testing.T) {
	expected := []byte{0, 13, 10, 255, 128, 0, 0, 0, 1, 17, 54, 91, 128, 165, 202, 239}
	if !bytes.Equal(payload(1, 16), expected) {
		t.Fatal("binary oracle changed")
	}
	if nativeMember("fence-1") != "W3vLCXAo" {
		t.Fatal("native member derivation changed")
	}
}
func TestNativeResponsePreservesBinaryFraming(t *testing.T) {
	for _, data := range [][]byte{[]byte("MSG _INBOX.X 1 5\r\n\x00\r\n\xff\x80\r\n"), []byte("MSG _INBOX.wrong 1 0\r\n\r\n"), []byte("MSG _INBOX.X 1 05\r\n12345\r\n")} {
		left, right := net.Pipe()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		done := make(chan struct{})
		go func() { defer close(done); defer right.Close(); right.Write(data) }()
		w := wire{conn: left, reader: bufio.NewReader(left), ctx: ctx}
		raw, body, err := w.response("_INBOX.X")
		if bytes.Contains(data, []byte("\x00")) {
			if err != nil || !bytes.Equal(raw, data) || !bytes.Equal(body, []byte{0, 13, 10, 255, 128}) {
				t.Fatal("binary response changed", err)
			}
		} else if err == nil {
			t.Fatal("accepted wrong correlation or size")
		}
		left.Close()
		cancel()
		<-done
	}
}
func TestRawWireRetainsAvailablePartialBytes(t *testing.T) {
	dir := t.TempDir()
	input, _ := os.Create(filepath.Join(dir, "in"))
	output, _ := os.Create(filepath.Join(dir, "out"))
	left, right := net.Pipe()
	c := &auditedConn{Conn: left, input: &boundedLog{file: input}, output: &boundedLog{file: output}}
	done := make(chan struct{})
	go func() { defer close(done); right.Write([]byte("MSG _INBOX.X 1 9\r\npart")); right.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	w := wire{conn: c, reader: bufio.NewReader(c), ctx: ctx}
	if _, _, err := w.response("_INBOX.X"); err == nil {
		t.Fatal("partial body accepted")
	}
	c.Close()
	<-done
	data, err := os.ReadFile(filepath.Join(dir, "in"))
	if err != nil || !bytes.HasSuffix(data, []byte("part")) {
		t.Fatal("available partial bytes lost", err)
	}
}
func TestOutputOverflowRetainsOnlyBoundedPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	log := boundedLog{file: file}
	data := bytes.Repeat([]byte{255}, (2<<20)+17)
	n, err := log.Write(data)
	file.Close()
	stat, statErr := os.Stat(path)
	if err == nil || statErr != nil || n != 2<<20 || stat.Size() != 2<<20 || log.observed != int64(len(data)) {
		t.Fatal("overflow was accepted or prefix/count lost")
	}
}
func TestSnapshotRejectsLinksAndExpiredBudget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	os.Mkdir(source, 0700)
	os.WriteFile(filepath.Join(source, "data"), []byte("native"), 0600)
	files, err := snapshotStore(context.Background(), source, filepath.Join(root, "valid"))
	if err != nil || len(files) != 1 || files[0].Path != "data" || files[0].Size != 6 || len(files[0].SHA256) != 64 {
		t.Fatal("snapshot inventory is incomplete", files, err)
	}
	os.WriteFile(filepath.Join(source, "data"), []byte("later source change"), 0600)
	retained, err := os.ReadFile(filepath.Join(root, "valid", "data"))
	if err != nil || string(retained) != "native" {
		t.Fatal("snapshot did not preserve original bytes", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := snapshotStore(ctx, source, filepath.Join(root, "expired")); err == nil {
		t.Fatal("expired snapshot accepted")
	}
	if err := os.Symlink("data", filepath.Join(source, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotStore(context.Background(), source, filepath.Join(root, "linked")); err == nil {
		t.Fatal("store symlink accepted")
	}
}
func TestReadAndPublicationDeadlineAreFinite(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	defer left.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := wire{conn: left, reader: bufio.NewReader(left), ctx: ctx}
	if w.send([]byte("PING\r\n")) == nil {
		t.Fatal("dispatch after cancellation")
	}
	if _, err := w.line(); err == nil {
		t.Fatal("read after cancellation")
	}
	file, err := os.CreateTemp(t.TempDir(), "small")
	if err != nil {
		t.Fatal(err)
	}
	file.Write([]byte("two"))
	file.Close()
	if _, err = regularBytes(file.Name(), 1); err == nil {
		t.Fatal("oversized file read")
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"time"
)

const jszPath = "/jsz?accounts=true&streams=true&consumers=true&direct-consumers=true&config=true&raft=true&offset=0&limit=16"

var accounts = []string{"APP", "SYS", "$G"}

type observation struct {
	Node   string `json:"node"`
	Path   string `json:"path"`
	Status int    `json:"status"`
	Body   []byte `json:"body"`
	Order  int64  `json:"order"`
}
type nodeCollection struct {
	Health    observation            `json:"health"`
	JSZ       observation            `json:"jsz"`
	Inventory map[string]observation `json:"raft_inventory"`
	Groups    map[string]observation `json:"groups"`
}
type collection struct {
	Start  int64                     `json:"started_order"`
	Finish int64                     `json:"finished_order"`
	Nodes  map[string]nodeCollection `json:"nodes"`
}

func (r *runtimeState) observe(index int, path string) (value observation, err error) {
	if r.fatal != nil {
		return value, r.fatal
	}
	if r.httpCount >= 2000 || r.ioBytes > 128<<20 {
		r.fatal = errors.New("monitoring evidence budget exceeded")
		return value, r.fatal
	}
	r.httpCount++
	value.Node, value.Path = fmt.Sprintf("image-S-%d", index+1), path
	// Every request, including a failed poll, has its own retained record.
	defer func() {
		value.Order = r.order.Add(1)
		record := struct {
			observation
			Error string `json:"error"`
		}{observation: value}
		if err != nil {
			record.Error = err.Error()
		}
		data, marshalErr := json.Marshal(record)
		persistence := marshalErr
		if marshalErr == nil {
			persistence = ownedWrite(filepath.Join(r.dir, "http", fmt.Sprintf("%04d.json", r.httpCount)), data)
		}
		if persistence != nil {
			r.fatal = persistence
		}
		err = errors.Join(err, persistence)
	}()
	ctx, cancel := context.WithTimeout(r.ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d%s", 19101+index, path), nil)
	if err != nil {
		return value, err
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("monitoring redirect rejected") }}
	response, err := client.Do(request)
	if err != nil {
		return value, err
	}
	value.Status = response.StatusCode
	value.Body, err = io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	err = errors.Join(err, response.Body.Close())
	r.ioBytes += int64(len(value.Body))
	if len(value.Body) > 1<<20 {
		value.Body = value.Body[:1<<20]
		r.fatal = errors.New("monitoring body limit exceeded")
		err = errors.Join(err, r.fatal)
	}
	return value, err
}

func object(data []byte) (map[string]any, error) {
	var value map[string]any
	err := strictJSON(data, &value)
	if err == nil && value == nil {
		err = errors.New("native JSON object missing")
	}
	return value, err
}
func number(value any) (int64, bool) {
	n, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	v, err := n.Int64()
	return v, err == nil
}
func nested(value map[string]any, keys ...string) (map[string]any, bool) {
	current := value
	for _, key := range keys {
		var ok bool
		current, ok = current[key].(map[string]any)
		if !ok {
			return nil, false
		}
	}
	return current, true
}
func accountGroup(data []byte, account, group string) (map[string]any, error) {
	value, err := object(data)
	if err != nil {
		return nil, err
	}
	row, ok := nested(value, account, group)
	if !ok {
		return nil, errors.New("native Raft account/group missing")
	}
	return row, nil
}
func nativeStream(data []byte) (map[string]any, error) {
	value, err := object(data)
	if err != nil {
		return nil, err
	}
	rows, ok := value["account_details"].([]any)
	if !ok || len(rows) != 1 {
		return nil, errors.New("complete APP inventory missing")
	}
	account, ok := rows[0].(map[string]any)
	if !ok || account["id"] != "APP" {
		return nil, errors.New("native APP identity missing")
	}
	streams, ok := account["stream_detail"].([]any)
	if !ok || len(streams) != 1 {
		return nil, errors.New("native FENCE inventory missing")
	}
	stream, ok := streams[0].(map[string]any)
	if !ok || stream["name"] != "FENCE" {
		return nil, errors.New("native FENCE identity changed")
	}
	return stream, nil
}
func (r *runtimeState) collect(count int, group string) (result collection, err error) {
	result = collection{Start: r.order.Add(1), Nodes: map[string]nodeCollection{}}
	for index := 0; index < count; index++ {
		name := fmt.Sprintf("image-S-%d", index+1)
		node := nodeCollection{Inventory: map[string]observation{}, Groups: map[string]observation{}}
		if node.Health, err = r.observe(index, "/healthz"); err != nil {
			return result, err
		}
		if node.JSZ, err = r.observe(index, jszPath); err != nil {
			return result, err
		}
		for _, account := range accounts {
			path := "/raftz?acc=" + url.QueryEscape(account)
			if node.Inventory[account], err = r.observe(index, path); err != nil {
				return result, err
			}
			target := ""
			if count == 3 {
				if account == "APP" {
					target = group
				}
				if account == "SYS" {
					target = "_meta_"
				}
			}
			if target != "" {
				if node.Groups[account+"/"+target], err = r.observe(index, path+"&group="+url.QueryEscape(target)); err != nil {
					return result, err
				}
			}
		}
		result.Nodes[name] = node
	}
	result.Finish = r.order.Add(1)
	return result, nil
}

// This is only a bounded polling filter. Python independently checks every raw
// field, complete inventories, and both final brackets before accepting a run.
func settled(result collection, count int, group string) bool {
	if len(result.Nodes) != count {
		return false
	}
	common := map[string]string{}
	for name, node := range result.Nodes {
		health, err := object(node.Health.Body)
		if err != nil || node.Health.Status != 200 || health["status"] != "ok" {
			return false
		}
		stream, err := nativeStream(node.JSZ.Body)
		if err != nil || node.JSZ.Status != 200 {
			return false
		}
		config, ok := nested(stream, "config")
		if !ok || config["sealed"] != true {
			return false
		}
		if count == 1 {
			continue
		}
		for key, observation := range node.Groups {
			account, target := "SYS", "_meta_"
			if key != "SYS/_meta_" {
				account, target = "APP", group
			}
			row, err := accountGroup(observation.Body, account, target)
			if err != nil || observation.Status != 200 || row["id"] != nativeMember(name) {
				return false
			}
			progress, ok := number(row["pindex"])
			if !ok {
				return false
			}
			for _, field := range []string{"committed", "applied"} {
				value, valid := number(row[field])
				if !valid || value != progress {
					return false
				}
			}
			for _, field := range []string{"ipq_proposal_len", "ipq_entry_len", "ipq_resp_len", "ipq_apply_len"} {
				value, valid := number(row[field])
				if !valid || value != 0 {
					return false
				}
			}
			for _, field := range []string{"paused", "catchup", "observer", "overrun", "write_error"} {
				if value, present := row[field]; present && value != false && value != "" {
					return false
				}
			}
			fingerprint := fmt.Sprintf("%v/%v/%v/%d", row["leader"], row["term"], row["pterm"], progress)
			if previous, exists := common[key]; exists && previous != fingerprint {
				return false
			}
			common[key] = fingerprint
		}
	}
	return count == 1 || len(common) == 2
}
func (r *runtimeState) settledCollection(count int, group string) (collection, error) {
	for {
		if err := r.alive(1); err != nil {
			return collection{}, err
		}
		value, err := r.collect(count, group)
		if err != nil {
			return value, err
		}
		if settled(value, count, group) {
			return value, nil
		}
		select {
		case <-r.ctx.Done():
			return value, r.ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

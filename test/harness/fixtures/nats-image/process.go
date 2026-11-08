package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const nativeBinary = "/usr/local/bin/nats-server"
const nativeSHA = "33f3fe3fed80b2b229bd9fe4b735257ad8583cd2349452a0c74f3d768b593e66"

type processRecord struct {
	Name       string   `json:"name"`
	Generation int      `json:"generation"`
	PID        int      `json:"pid"`
	StartTicks uint64   `json:"start_ticks"`
	InfoID     string   `json:"info_id"`
	MemberID   string   `json:"member_id"`
	Argv       []string `json:"argv"`
	Config     []byte   `json:"config_bytes"`
	Store      string   `json:"store"`
	Device     uint64   `json:"store_device"`
	Inode      uint64   `json:"store_inode"`
	Started    int64    `json:"started_order"`
	Exited     int64    `json:"exited_order"`
	ExitCode   int      `json:"exit_code"`
	WaitError  *string  `json:"wait_error"`
	Forced     bool     `json:"forced_kill"`
}

type boundedLog struct {
	file              *os.File
	written, observed int64
}

func (b *boundedLog) Write(data []byte) (int, error) {
	b.observed += int64(len(data))
	remaining := int64(2*1024*1024) - b.written
	prefix := data
	if int64(len(prefix)) > remaining {
		prefix = prefix[:remaining]
	}
	n, err := b.file.Write(prefix)
	b.written += int64(n)
	if err != nil {
		return n, err
	}
	if len(prefix) != len(data) {
		return n, errors.New("native output limit exceeded")
	}
	return n, nil
}

type nativeChild struct {
	cmd            *exec.Cmd
	done           chan struct{}
	record         *processRecord
	requested      atomic.Bool
	stdout, stderr *boundedLog
	err            error
}

type runtimeState struct {
	ctx       context.Context
	dir       string
	order     atomic.Int64
	children  []*nativeChild
	wires     []*wire
	httpCount int
	ioBytes   int64
	fatal     error
}

func nativeMember(name string) string {
	digest := sha256.Sum256([]byte(name))
	alphabet := "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	result := make([]byte, 8)
	for i := range result {
		result[i] = alphabet[digest[i]%62]
	}
	return string(result)
}

func regularBytes(path string, maximum int64) ([]byte, error) {
	stat, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() || stat.Size() < 0 || stat.Size() > maximum {
		return nil, errors.New("nonregular or oversized input")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(stat, opened) {
		return nil, errors.New("input identity changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || int64(len(data)) != stat.Size() || after.Size() != stat.Size() || !after.ModTime().Equal(stat.ModTime()) {
		return nil, errors.New("input changed during read")
	}
	return data, nil
}

func ownedWrite(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, written := file.Write(data)
	synced := file.Sync()
	closed := file.Close()
	return errors.Join(written, synced, closed)
}

func (r *runtimeState) start(generation, index int, config []byte) (*nativeChild, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("image-S-%d", index+1)
	store := filepath.Join(r.dir, "stores", name)
	if generation == 0 {
		if err := os.Mkdir(store, 0700); err != nil {
			return nil, err
		}
	}
	stat, err := os.Lstat(store)
	if err != nil || !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("native store is not the original directory")
	}
	identity, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, errors.New("native store identity unavailable")
	}
	configPath := filepath.Join(r.dir, "config", fmt.Sprintf("%d-%s.conf", generation, name))
	if err = ownedWrite(configPath, config); err != nil {
		return nil, err
	}
	record := &processRecord{Name: name, Generation: generation, MemberID: nativeMember(name), Config: config,
		Store: store, Device: uint64(identity.Dev), Inode: uint64(identity.Ino), ExitCode: -1,
		Argv: []string{nativeBinary, "--config", configPath}}
	child := &nativeChild{cmd: exec.Command(record.Argv[0], record.Argv[1:]...), done: make(chan struct{}), record: record}
	for _, output := range []struct {
		label  string
		target **boundedLog
	}{{"stdout", &child.stdout}, {"stderr", &child.stderr}} {
		file, err := os.OpenFile(filepath.Join(r.dir, "logs", fmt.Sprintf("%d-%s.%s", generation, name, output.label)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			if child.stdout != nil {
				child.stdout.file.Close()
			}
			return nil, err
		}
		*output.target = &boundedLog{file: file}
	}
	child.cmd.Stdout, child.cmd.Stderr = child.stdout, child.stderr
	if err = child.cmd.Start(); err != nil {
		child.stdout.file.Close()
		child.stderr.file.Close()
		return nil, err
	}
	record.PID, record.Started = child.cmd.Process.Pid, r.order.Add(1)
	r.children = append(r.children, child)
	// The owned handle is retained until Wait; never signal a PID read from evidence.
	go func() {
		child.err = child.cmd.Wait()
		record.Exited = r.order.Add(1)
		record.ExitCode = child.cmd.ProcessState.ExitCode()
		if !child.requested.Load() {
			child.err = errors.Join(child.err, errors.New("native child exited before requested stop"))
		}
		for _, log := range []*boundedLog{child.stdout, child.stderr} {
			child.err = errors.Join(child.err, log.file.Sync(), log.file.Close())
		}
		if child.err != nil {
			message := child.err.Error()
			record.WaitError = &message
		}
		close(child.done)
	}()
	data, err := regularBytes(fmt.Sprintf("/proc/%d/stat", record.PID), 65536)
	// procfs reports size zero; use a bounded read for this specific native identity.
	if err != nil {
		file, openErr := os.Open(fmt.Sprintf("/proc/%d/stat", record.PID))
		if openErr != nil {
			return nil, openErr
		}
		data, err = io.ReadAll(io.LimitReader(file, 65537))
		closeErr := file.Close()
		if err != nil || closeErr != nil || len(data) > 65536 {
			return nil, errors.Join(err, closeErr, errors.New("native process identity read failed"))
		}
	}
	end := strings.LastIndex(string(data), ")")
	if end < 0 {
		return nil, errors.New("invalid native process stat")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return nil, errors.New("incomplete native process stat")
	}
	record.StartTicks, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil || record.StartTicks == 0 {
		return nil, errors.New("invalid native start identity")
	}
	return child, nil
}

func (c *nativeChild) stop() error {
	select {
	case <-c.done:
		return c.err
	default:
	}
	c.requested.Store(true)
	signalErr := c.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-c.done:
		return errors.Join(c.err, signalErr)
	case <-time.After(5 * time.Second):
	}
	c.record.Forced = true
	if err := c.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	select {
	case <-c.done:
		return errors.Join(c.err, signalErr, errors.New("native child required forced termination"))
	case <-time.After(5 * time.Second):
		return errors.New("native child Wait unresolved")
	}
}

func (r *runtimeState) stopAll(generation int) error {
	var result error
	for _, child := range r.children {
		if generation < 0 || child.record.Generation == generation {
			result = errors.Join(result, child.stop())
		}
	}
	return result
}

func (r *runtimeState) alive(generation int) error {
	if r.fatal != nil {
		return r.fatal
	}
	for _, child := range r.children {
		if child.record.Generation == generation {
			select {
			case <-child.done:
				return errors.New("native child exited during the active phase")
			default:
			}
		}
	}
	return r.ctx.Err()
}

type snapshotFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size_bytes"`
}

type storeSnapshot struct {
	Name     string         `json:"name"`
	Started  int64          `json:"started_order"`
	Finished int64          `json:"finished_order"`
	Files    []snapshotFile `json:"files"`
}

func snapshotStore(ctx context.Context, source, destination string) ([]snapshotFile, error) {
	var total int64
	files := []snapshotFile{}
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, visitErr error) error {
		if visitErr != nil {
			return visitErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.Mkdir(target, 0700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("native store contains a symlink")
		}
		if len(files) >= 8192 {
			return errors.New("store snapshot file count exceeded")
		}
		data, err := regularBytes(path, 64*1024*1024)
		if err != nil {
			return err
		}
		total += int64(len(data))
		if total > 128*1024*1024 {
			return errors.New("store snapshot exceeds bound")
		}
		if err = ownedWrite(target, data); err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		files = append(files, snapshotFile{Path: filepath.ToSlash(relative), SHA256: hex.EncodeToString(digest[:]), Size: int64(len(data))})
		return nil
	})
	return files, err
}

func verifyNative() error {
	data, err := regularBytes(nativeBinary, 128*1024*1024)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != nativeSHA {
		return errors.New("native image executable changed")
	}
	return nil
}

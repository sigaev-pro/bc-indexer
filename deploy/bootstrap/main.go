// The bootstrap of the deployment stack (E06-13 deployment pre-build):
// it issues the immutable DestinationBinding of each network, generates
// the private CA, the service certificates and the NATS account
// configuration, creates the per-network JetStream streams with the two
// D77 identity keys, and verifies the live target with the runtime
// credentials.
//
// Bootstrap credentials differ from runtime credentials: the bootstrap
// users hold stream administration inside their account, the runtime
// gateway users can only publish into the closed subject namespace of
// their own network, ask for account and stream information and answer
// on their reply inbox. Runtime users never create, update, delete or
// purge anything (DEPLOYMENT_BOOTSTRAP_CONTRACT, "Trust bootstrap and
// NATS permissions").
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sigaev-pro/bc-indexer/deploy/internal/deployconfig"
	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

func main() {
	configDir := flag.String("config-dir", "config", "directory of the strict per-network configuration")
	secretsDir := flag.String("secrets-dir", "secrets", "directory of the generated trust material and credentials")
	server := flag.String("server", "tls://127.0.0.1:4222", "NATS address of the deployment")
	flag.Parse()
	args := flag.Args()
	if len(args) != 1 {
		usage()
	}
	// Commands that touch a live server need the generated CA.
	switch args[0] {
	case "bindings":
		if err := cmdBindings(*configDir); err != nil {
			fail(err)
		}
	case "secrets":
		if err := cmdSecrets(*configDir, *secretsDir); err != nil {
			fail(err)
		}
	case "streams":
		if err := withBootstrapClients(*secretsDir, *server, func(n deployconfig.NetworkDefinition, nc *nats.Conn) error {
			return ensureStream(n, nc)
		}); err != nil {
			fail(err)
		}
	case "verify":
		if err := cmdVerify(*configDir, *secretsDir, *server); err != nil {
			fail(err)
		}
	case "peek":
		if err := cmdPeek(*secretsDir, *server); err != nil {
			fail(err)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: bootstrap [-config-dir DIR] [-secrets-dir DIR] [-server URL] {bindings|secrets|streams|verify|peek}")
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "bootstrap: %v\n", err)
	os.Exit(1)
}

// cmdBindings writes the deterministic binding descriptor of every
// network and cross-validates each file with the authoritative
// destination implementation of the repository. Regeneration is
// byte-stable: an existing file that differs is an operator error.
func cmdBindings(configDir string) error {
	for _, n := range deployconfig.Networks {
		b, err := deployconfig.GenerateBinding(n)
		if err != nil {
			return err
		}
		data, err := b.MarshalJSON()
		if err != nil {
			return err
		}
		data = append(data, '\n')
		path := fmt.Sprintf("%s/%s/binding.json", configDir, n.Key)
		if existing, err := os.ReadFile(path); err == nil && string(existing) == string(data) {
			fmt.Printf("%s binding %s: unchanged (destination_id %s)\n", n.Key, path, b.DestinationIDHex())
		} else if err == nil {
			return fmt.Errorf("%s exists with different content; inspect it and remove it before regenerating", path)
		} else if !os.IsNotExist(err) {
			return err
		} else {
			if err := os.MkdirAll(fmt.Sprintf("%s/%s", configDir, n.Key), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				return err
			}
			fmt.Printf("%s binding %s: written (destination_id %s)\n", n.Key, path, b.DestinationIDHex())
		}
		if err := crossValidate(path, data); err != nil {
			return err
		}
		fmt.Printf("%s network_digest %s destination_id %s\n", n.Key, b.NetworkDigestHex(), b.DestinationIDHex())
	}
	return nil
}

// crossValidate parses the descriptor with the repository's
// destination.ParseBinding, the authoritative strict reader of the
// contract: any drift between the deploy reader, the digest formulas and
// the repository implementation stops bootstrap here.
func crossValidate(path string, data []byte) error {
	b, err := destination.ParseBinding(data)
	if err != nil {
		return fmt.Errorf("authoritative validation of %s: %w", path, err)
	}
	digest, err := b.NetworkDigest()
	if err != nil {
		return err
	}
	fmt.Printf("%s authoritative destination check: ok (network digest %x)\n", path, digest)
	return nil
}

// withBootstrapClients connects to NATS once per network with that
// network's bootstrap user and runs fn.
func withBootstrapClients(secretsDir, server string, fn func(n deployconfig.NetworkDefinition, nc *nats.Conn) error) error {
	for _, n := range deployconfig.Networks {
		nc, err := dial(secretsDir, server, fmt.Sprintf("%s/nats/bootstrap-%s.creds", secretsDir, n.Key), "nats")
		if err != nil {
			return fmt.Errorf("network %s bootstrap connection: %w", n.Key, err)
		}
		err = fn(n, nc)
		nc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// ensureStream creates the network stream with the two D77 identity
// keys, or verifies an existing stream. A CREATE that meets an existing
// stream takes the verification branch; nothing is updated, deleted or
// purged automatically (D77: missing, malformed or mismatched identity
// is CONFIG_BLOCKED, never an automatic repair).
func ensureStream(n deployconfig.NetworkDefinition, nc *nats.Conn) error {
	b, err := deployconfig.GenerateBinding(n)
	if err != nil {
		return err
	}
	js, err := nc.JetStream(nats.MaxWait(10 * time.Second))
	if err != nil {
		return err
	}
	_, err = js.AddStream(&nats.StreamConfig{
		Name:       b.Stream,
		Subjects:   []string{n.SubjectPrefix + ".>"},
		Storage:    nats.FileStorage,
		MaxMsgSize: int32(deployconfig.MaxFrameBytesDefault),
		Metadata: map[string]string{
			"extractor.network_digest": b.NetworkDigestHex(),
			"extractor.destination_id": b.DestinationIDHex(),
		},
	})
	if err == nil {
		fmt.Printf("%s stream %s: created with identity metadata\n", n.Key, b.Stream)
	} else if !strings.Contains(err.Error(), "stream name already in use") {
		return fmt.Errorf("network %s create stream: %w", n.Key, err)
	} else {
		fmt.Printf("%s stream %s: exists, verifying\n", n.Key, b.Stream)
	}
	// Read-back verification of the identity, subjects and limits before
	// bootstrap reports success (D77: CREATE requires a read-back).
	info, err := js.StreamInfo(b.Stream)
	if err != nil {
		return fmt.Errorf("network %s read back stream: %w", n.Key, err)
	}
	return verifyStreamInfo(n, b, info)
}

// verifyStreamInfo checks the read-back StreamInfo against the binding.
func verifyStreamInfo(n deployconfig.NetworkDefinition, b deployconfig.Binding, info *nats.StreamInfo) error {
	var reasons []string
	if info.Config.Name != b.Stream {
		reasons = append(reasons, fmt.Sprintf("stream name %q differs from %q", info.Config.Name, b.Stream))
	}
	covered := false
	for _, pat := range info.Config.Subjects {
		if pat == n.SubjectPrefix+".>" {
			covered = true
		}
	}
	if !covered {
		reasons = append(reasons, fmt.Sprintf("subjects %v do not cover %s.>", info.Config.Subjects, n.SubjectPrefix))
	}
	if info.Config.MaxMsgSize != int32(deployconfig.MaxFrameBytesDefault) {
		reasons = append(reasons, fmt.Sprintf("max message size is %d, want %d",
			info.Config.MaxMsgSize, deployconfig.MaxFrameBytesDefault))
	}
	if got := info.Config.Metadata["extractor.network_digest"]; got != b.NetworkDigestHex() {
		reasons = append(reasons, fmt.Sprintf("metadata extractor.network_digest is %q, want %q", got, b.NetworkDigestHex()))
	}
	if got := info.Config.Metadata["extractor.destination_id"]; got != b.DestinationIDHex() {
		reasons = append(reasons, fmt.Sprintf("metadata extractor.destination_id is %q, want %q", got, b.DestinationIDHex()))
	}
	if len(reasons) > 0 {
		return fmt.Errorf("network %s stream %s: CONFIG_BLOCKED: %s", n.Key, b.Stream, strings.Join(reasons, "; "))
	}
	fmt.Printf("%s stream %s: verified (messages %d)\n", n.Key, b.Stream, info.State.Msgs)
	return nil
}

// cmdVerify runs the runtime verification: for every network it connects
// with the runtime gateway credentials and performs the authoritative
// destination.Check over the live target, then reports the stream state.
func cmdVerify(configDir, secretsDir, server string) error {
	ok := true
	for _, n := range deployconfig.Networks {
		b, err := deployconfig.LoadBindingFile(fmt.Sprintf("%s/%s/binding.json", configDir, n.Key))
		if err != nil {
			return err
		}
		nc, err := dial(secretsDir, server, fmt.Sprintf("%s/%s/nats.creds", secretsDir, n.Key), "nats")
		if err != nil {
			fmt.Printf("%s: connection failed: %v\n", n.Key, err)
			ok = false
			continue
		}
		report := destination.Check(context.Background(), &natsRemote{nc: nc}, toDestinationBinding(b), n.EventSubjects(), deployconfig.MaxFrameBytesDefault)
		js, _ := nc.JetStream(nats.MaxWait(10 * time.Second))
		info, infoErr := js.StreamInfo(b.Stream)
		nc.Close()
		switch report.Status {
		case destination.StatusVerified:
			state := "state unavailable"
			if infoErr == nil {
				stateBytes, _ := json.Marshal(map[string]any{
					"messages": info.State.Msgs, "bytes": info.State.Bytes, "first_seq": info.State.FirstSeq,
				})
				state = string(stateBytes)
			}
			fmt.Printf("%s: VERIFIED account=%s stream=%s %s\n", n.Key, b.ExpectedAccount, b.Stream, state)
		case destination.StatusConfigBlocked:
			fmt.Printf("%s: CONFIG_BLOCKED reasons=%v\n", n.Key, report.Reasons)
			ok = false
		default:
			fmt.Printf("%s: UNAVAILABLE reasons=%v\n", n.Key, report.Reasons)
			ok = false
		}
	}
	if !ok {
		return fmt.Errorf("verification failed")
	}
	return nil
}

// cmdPeek prints the last message of every event route of one or all
// networks, using the bootstrap credentials of that network.
func cmdPeek(secretsDir, server string) error {
	return withBootstrapClients(secretsDir, server, func(n deployconfig.NetworkDefinition, nc *nats.Conn) error {
		js, err := nc.JetStream(nats.MaxWait(10 * time.Second))
		if err != nil {
			return err
		}
		b, err := deployconfig.GenerateBinding(n)
		if err != nil {
			return err
		}
		for _, subject := range n.EventSubjects() {
			msg, err := js.GetLastMsg(b.Stream, subject)
			if err != nil {
				if strings.Contains(err.Error(), "message not found") {
					fmt.Printf("%s %s: empty\n", n.Key, subject)
					continue
				}
				return err
			}
			fmt.Printf("%s %s: seq=%d time=%s msg_id=%s payload=%s\n",
				n.Key, subject, msg.Sequence, msg.Time.UTC().Format(time.RFC3339), msg.Header.Get("Nats-Msg-Id"), string(msg.Data))
		}
		return nil
	})
}

// toDestinationBinding converts the deploy binding into the
// authoritative destination.Binding of the repository.
func toDestinationBinding(b deployconfig.Binding) destination.Binding {
	var out destination.Binding
	out.DestinationID = b.DestinationID
	out.Generation = b.Generation
	out.Network.Family = proto.NetworkFamilyEVM
	out.Network.ChainID = b.ChainID
	out.Network.GenesisHash = b.GenesisHash
	out.ExpectedAccount = b.ExpectedAccount
	out.Stream = b.Stream
	out.SubjectPrefix = b.SubjectPrefix
	out.DescriptorDigest = b.DescriptorDigest
	return out
}

// dial connects with the TLS CA of the deployment and a two-line
// credentials file, verifying the NATS server certificate.
func dial(secretsDir, server, credsPath, serverName string) (*nats.Conn, error) {
	caPEM, err := os.ReadFile(fmt.Sprintf("%s/ca.crt", secretsDir))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("ca.crt holds no certificate")
	}
	creds, err := readCreds(credsPath)
	if err != nil {
		return nil, err
	}
	return nats.Connect(server,
		nats.Secure(&tls.Config{RootCAs: pool, ServerName: serverName, MinVersion: tls.VersionTLS12}),
		nats.UserInfo(creds[0], creds[1]),
		nats.MaxReconnects(2),
		nats.ReconnectWait(time.Second),
	)
}

// readCreds reads the two-line username/password credentials file.
func readCreds(path string) ([2]string, error) {
	var out [2]string
	data, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || lines[0] == "" || lines[1] == "" {
		return out, fmt.Errorf("%s must hold the username and the password on two lines", path)
	}
	out[0], out[1] = lines[0], lines[1]
	return out, nil
}

// natsRemote adapts the pinned nats.go client surface to the
// destination.Remote seam: account information and stream information.
type natsRemote struct {
	nc *nats.Conn
}

// AccountInfo returns the exact account field of the UserInfo answer,
// never the display name.
func (r *natsRemote) AccountInfo(ctx context.Context) (destination.AccountInfo, error) {
	resp, err := r.nc.RequestWithContext(ctx, "$SYS.REQ.USER.INFO", []byte("{}"))
	if err != nil {
		return destination.AccountInfo{}, err
	}
	var answer struct {
		Account string `json:"account"`
		Data    struct {
			Account string `json:"account"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Data, &answer); err != nil {
		return destination.AccountInfo{}, fmt.Errorf("user info answer: %w", err)
	}
	account := answer.Account
	if account == "" {
		account = answer.Data.Account
	}
	if account == "" {
		return destination.AccountInfo{}, fmt.Errorf("user info answer holds no account field")
	}
	return destination.AccountInfo{Account: account}, nil
}

// StreamInfo returns the configuration of the named stream.
func (r *natsRemote) StreamInfo(ctx context.Context, stream string) (destination.StreamInfo, error) {
	js, err := r.nc.JetStream()
	if err != nil {
		return destination.StreamInfo{}, err
	}
	info, err := js.StreamInfo(stream, nats.Context(ctx))
	if err != nil {
		return destination.StreamInfo{}, err
	}
	return destination.StreamInfo{
		Name:       info.Config.Name,
		Subjects:   info.Config.Subjects,
		Metadata:   info.Config.Metadata,
		MaxMsgSize: uint64(info.Config.MaxMsgSize),
	}, nil
}

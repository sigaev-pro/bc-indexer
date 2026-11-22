// Mock producer: generates realistic-looking blockchain events and
// publishes them to NATS JetStream for the demo environment.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

type TxEvent struct {
	Network  string `json:"network"`
	Block    int64  `json:"block"`
	TxHash   string `json:"tx_hash"`
	From     string `json:"from_addr"`
	To       string `json:"to_addr"`
	Value    string `json:"value"`
	Type     string `json:"type"`
	Status   string `json:"status"`
}

type BalanceEvent struct {
	Network      string `json:"network"`
	Block        int64  `json:"block"`
	Address      string `json:"address"`
	BalanceAfter string `json:"balance_after"`
	Change       string `json:"change"`
}

var addrChars = "0123456789abcdef"

func randomHex(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = addrChars[rand.Intn(len(addrChars))]
	}
	return "0x" + string(b)
}

func main() {
	natsURL := env("NATS_URL", "nats://localhost:4222")
	networks := strings.Split(env("NETWORKS", "ETH,BSC,POLY"), ",")
	interval, _ := time.ParseDuration(env("TX_INTERVAL", "2s"))

	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatal(err)
	}
	defer nc.Close()

	// Ensure streams exist
	for _, net := range networks {
		streamName := "EXTRACTOR-" + strings.ToUpper(net)
		subjects := []string{
			fmt.Sprintf("extractor.%s.transaction", strings.ToLower(net)),
			fmt.Sprintf("extractor.%s.balance", strings.ToLower(net)),
		}
		_, err := nc.JetStream().AddStream(&nats.StreamConfig{
			Name:     streamName,
			Subjects: subjects,
			Storage:  nats.FileStorage,
		})
		if err != nil && !strings.Contains(err.Error(), "already exists") {
			log.Printf("stream %s: %v", streamName, err)
		}
	}

	block := int64(1)
	log.Printf("Mock producer started: networks=%v interval=%v", networks, interval)

	for {
		for _, net := range networks {
			// Generate a transaction
			tx := TxEvent{
				Network: net,
				Block:   block,
				TxHash:  randomHex(64),
				From:    randomHex(40),
				To:      randomHex(40),
				Value:   fmt.Sprintf("%d", rand.Intn(1000000000000000000)),
				Type:    []string{"transfer", "contract_call", "contract_deploy"}[rand.Intn(3)],
				Status:  []string{"success", "success", "success", "reverted"}[rand.Intn(4)],
			}
			txData, _ := json.Marshal(tx)
			subject := fmt.Sprintf("extractor.%s.transaction", strings.ToLower(net))
			nc.Publish(subject, txData)

			// Generate a balance event
			bal := BalanceEvent{
				Network:      net,
				Block:        block,
				Address:      tx.From,
				BalanceAfter: fmt.Sprintf("%d", rand.Intn(10000000000000000000)),
				Change:       "-" + tx.Value,
			}
			balData, _ := json.Marshal(bal)
			balSubject := fmt.Sprintf("extractor.%s.balance", strings.ToLower(net))
			nc.Publish(balSubject, balData)
		}

		block++
		time.Sleep(interval)
	}
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

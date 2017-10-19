package main

import (
	// Stdlib

	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	// RPC

	"github.com/shaunmza/steemgo"
	"github.com/shaunmza/steemgo/encoding/wif"
	"github.com/shaunmza/steemgo/transactions"
	"github.com/shaunmza/steemgo/transports/websocket"
	"github.com/shaunmza/steemgo/types"
	// Vendor
)

type transfer struct {
	To     string
	Amount string
	Memo   string
}

type transferIn struct {
	Player string
	Amount float64
	Memo   string
}

type status struct {
	Message  string
	Transfer *transfer
}

type output struct {
	Status []status
	Error  string
}

//Config struct
type Config struct {
	WitnessUrl        string
	PrivateKey        string
	Port              string
	AccountName       string
	Currency          string
	MinimumReputation int
	LogFile           string
}

type transferStatus struct {
	Account string
	Status  string
	Error   string
}

var client *steemgo.Client
var c *Config

func main() {
	var err error
	var cFile *string

	m := "Absolute path to config file. ie -config=/config.json"
	cFile = flag.String("config", "", m)

	mT := "Absolute path to file. ie -csv=/file.{csv,json}"
	tFile := flag.String("csv", "", mT)

	flag.Parse()

	// Make sure we have a config file
	if *cFile == "" {
		panic(fmt.Sprintf("Error: %s", m))
	}

	// Make sure we have a csv file
	if *tFile == "" {
		panic(fmt.Sprintf("Error: %s", mT))
	}

	c = loadConfig(*cFile)

	_ = http.Server{ReadTimeout: 10 * time.Second}
	addr, err := net.ResolveTCPAddr("tcp", ":"+c.Port)
	_, err = net.ListenTCP("tcp", addr)

	if err != nil {
		if err.Error() == "listen tcp :"+c.Port+": bind: address already in use" {
			fmt.Println("Already running")
		} else {
			panic(err)
		}
		os.Exit(3)
	}
	fmt.Println("Running...")

	f, err := os.Create(c.LogFile)
	if err != nil {
		panic(fmt.Sprintf("Cannot create log file: %s", c.LogFile))
	}
	f.Close()

	logLine("[")

	t := *tFile
	if strings.Contains(*tFile, ".csv") {
		importCsv(t)
	} else {
		importJson(t)
	}
	logLine("]")

	fmt.Println("Done!")

}

func send(tr *transfer, client *steemgo.Client) (res string) {
	config, err := client.Database.GetConfig()
	if err != nil {
		return "Could not connect (configs)"
	}

	// Get the props to get the head block number and ID
	// so that we can use that for the transaction.
	props, err := client.Database.GetDynamicGlobalProperties()
	if err != nil {
		return "Could not connect (properties)"
	}

	// Prepare the transaction.
	refBlockPrefix, err := transactions.RefBlockPrefix(props.HeadBlockID)
	if err != nil {
		return "Could not connect (block prefix)"
	}

	tx := transactions.NewSignedTransaction(&types.Transaction{
		RefBlockNum:    transactions.RefBlockNum(props.HeadBlockNumber),
		RefBlockPrefix: refBlockPrefix,
	})

	tx.PushOperation(&types.TransferOperation{
		From:   c.AccountName,
		To:     tr.To,
		Amount: tr.Amount,
		Memo:   tr.Memo,
	})

	// Sign.
	privKey, err := wif.Decode(c.PrivateKey)
	if err != nil {
		return "Could not decode the WIF key, please make sure you supplied the correct private key " + err.Error()
	}
	privKeys := [][]byte{privKey}

	if err := tx.Sign(privKeys, transactions.SteemChain); err != nil {
		return "Could not sign error is: " + err.Error()
	}

	// Broadcast.
	_, err = client.NetworkBroadcast.BroadcastTransactionSynchronous(tx.Transaction)
	if err != nil {
		return "Could not broadcast error is: " + err.Error()
	}

	time.Sleep(time.Duration(config.SteemitBlockInterval) * time.Second)

	// Success!
	return "Sent"
}

func importCsv(csvFile string) {

	f, err := os.Open(csvFile)

	if err != nil {
		panic(fmt.Sprintf("Failed to open csv file: %v\n", err))
	}

	defer f.Close()

	r := csv.NewReader(f)
	r.Comma = '\t'

	// Start catching signals.
	var interrupted bool
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)

	// Instantiate the WebSocket transport.
	t, err := websocket.NewTransport(c.WitnessUrl)
	if err != nil {
		fmt.Println("Could not connect (websocket)")
		return
	}

	// Use the transport to get an RPC client.
	client, err := steemgo.NewClient(t)
	if err != nil {
		fmt.Println("Could not connect (client)")
		return
	}
	defer func() {
		if !interrupted {
			client.Close()
		}
	}()

	// Start processing signals.
	go func() {
		<-signalCh
		fmt.Println()
		log.Println("Signal received, exiting...")
		logLine("]")
		signal.Stop(signalCh)
		interrupted = true
		client.Close()
	}()

	if err == nil {
		for {
			record, err := r.Read()
			if err == io.EOF {
				break
			}

			st := transferStatus{}

			if err != nil {
				log.Fatal(err)
			}
			var ac []string
			ac = append(ac, strings.ToLower(strings.TrimSpace(record[0])))
			st.Account = record[0]

			acc, err := client.Database.GetAccounts(ac)
			if err != nil {
				st.Status = "Failed"
				st.Error = "Could not fetch user details from the blockchain"
				ln, err := json.Marshal(st)
				if err != nil {
					fmt.Println("Transfer failed: " + err.Error())
					return
				}

				err = logLine(string(ln[:]))
				if err != nil {
					fmt.Println("Transfer failed: " + err.Error())
					return
				}
				continue
			}

			rp := acc[0].Reputation
			rpSimple := calcReputation(rp)

			if rpSimple < c.MinimumReputation {
				st.Status = "Failed"
				st.Error = fmt.Sprintf("User reputation (%d) below Minimum reputation of %d", rpSimple, c.MinimumReputation)

				ln, err := json.Marshal(st)
				if err != nil {
					fmt.Println("Transfer failed: " + err.Error())
					return
				}

				err = logLine(string(ln[:]))
				if err != nil {
					fmt.Println("Transfer failed: " + err.Error())
					return
				}
				continue
			}

			tr := &transfer{To: ac[0],
				Amount: strings.TrimSpace(record[1]) + " " + c.Currency,
				Memo:   strings.TrimSpace(record[2])}

			r := send(tr, client)
			if r == "Sent" {
				st.Status = "Success"

				ln, err := json.Marshal(st)
				if err != nil {
					fmt.Println("Transfer failed: " + err.Error())
					return
				}

				err = logLine(string(ln[:]))
				if err != nil {
					fmt.Println("Transfer failed: " + err.Error())
					return
				}

			} else {
				st.Status = "Failed"
				st.Error = r

				ln, err := json.Marshal(st)
				if err != nil {
					fmt.Println("Transfer failed: " + err.Error())
					return
				}

				err = logLine(string(ln[:]))
				if err != nil {
					fmt.Println("Transfer failed: " + err.Error())
					return
				}
			}
		}
	}

}

func importJson(jsonFile string) {

	f, err := ioutil.ReadFile(jsonFile)

	if err != nil {
		panic(fmt.Sprintf("Failed to open json file: %v\n", err))
	}

	// Start catching signals.
	var interrupted bool
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, syscall.SIGINT, syscall.SIGTERM)

	// Instantiate the WebSocket transport.
	t, err := websocket.NewTransport(c.WitnessUrl)
	if err != nil {
		fmt.Println("Could not connect (websocket)")
		return
	}

	// Use the transport to get an RPC client.
	client, err := steemgo.NewClient(t)
	if err != nil {
		fmt.Println("Could not connect (client)")
		return
	}
	defer func() {
		if !interrupted {
			client.Close()
		}
	}()

	// Start processing signals.
	go func() {
		<-signalCh
		fmt.Println()
		log.Println("Signal received, exiting...")
		logLine("]")
		signal.Stop(signalCh)
		interrupted = true
		client.Close()
	}()

	records := make([]*transfer, 0)
	err = json.Unmarshal(f, &records)
	if err != nil {
		fmt.Println("Could not read file: " + err.Error())
		return
	}

	for _, record := range records {
		st := transferStatus{}

		if err != nil {
			fmt.Println("Transfer failed: " + err.Error())
			return
		}

		st.Account = record.To

		var ac []string
		ac = append(ac, strings.ToLower(strings.TrimSpace(record.To)))

		acc, err := client.Database.GetAccounts(ac)
		if err != nil {
			st.Status = "Failed"
			st.Error = "Could not fetch user details from the blockchain"
			ln, err := json.Marshal(st)
			if err != nil {
				fmt.Println("Transfer failed: " + err.Error())
				return
			}

			err = logLine(string(ln[:]))
			if err != nil {
				fmt.Println("Transfer failed: " + err.Error())
				return
			}
			continue
		}

		if len(acc) == 0 {
			st.Status = "Failed"
			st.Error = "User not found:" + ac[0]
			ln, err := json.Marshal(st)
			if err != nil {
				fmt.Println("Transfer failed: " + err.Error())
				return
			}

			err = logLine(string(ln[:]))
			if err != nil {
				fmt.Println("Transfer failed: " + err.Error())
				return
			}
			continue
		}

		rp := acc[0].Reputation
		rpSimple := calcReputation(rp)

		if rpSimple < c.MinimumReputation {
			st.Status = "Failed"
			st.Error = fmt.Sprintf("User reputation (%d) below Minimum reputation of %d\n", rpSimple, c.MinimumReputation)

			ln, err := json.Marshal(st)
			if err != nil {
				fmt.Println("Transfer failed: " + err.Error())
				return
			}

			err = logLine(string(ln[:]))
			if err != nil {
				fmt.Println("Transfer failed: " + err.Error())
				return
			}
			continue
		}

		tr := &transfer{To: ac[0],
			Amount: strings.TrimSpace(record.Amount) + " " + c.Currency,
			Memo:   strings.TrimSpace(record.Memo)}

		r := send(tr, client)
		if r == "Sent" {
			st.Status = "Success"

			ln, err := json.Marshal(st)
			if err != nil {
				fmt.Println("Transfer failed: " + err.Error())
				return
			}

			err = logLine(string(ln[:]))
			if err != nil {
				fmt.Println("Transfer failed: " + err.Error())
				return
			}

		} else {
			st.Status = "Failed"
			st.Error = r

			ln, err := json.Marshal(st)
			if err != nil {
				fmt.Println("Transfer failed: " + err.Error())
				return
			}

			err = logLine(string(ln[:]))
			if err != nil {
				fmt.Println("Transfer failed: " + err.Error())
				return
			}
		}

	}

}

func (t *transfer) UnmarshalJSON(data []byte) (err error) {
	var raw transferIn
	if err := json.NewDecoder(strings.NewReader(string(data))).Decode(&raw); err != nil {
		return err
	}

	t.To = raw.Player
	t.Memo = raw.Memo
	t.Amount = strconv.FormatFloat(raw.Amount, 'f', 6, 64)

	return nil
}

func calcReputation(rep string) int {

	if rep == "" {
		return 0
	}

	neg := strings.Contains(rep, "-")
	rep = strings.Replace(rep, "-", "", 1)

	leadingDigits, err := strconv.Atoi(rep[:4])
	if err != nil {
		return 0
	}

	lg := math.Log(float64(leadingDigits)) / math.Log(10)
	n := float64(len(rep) - 1)
	out := n + (lg - float64(int(lg)))

	out = math.Max(out-9, 0)
	if neg {
		out = out * -1
	}
	out = out*9 + 25
	ret := int(out)

	return ret
}

func loadConfig(cFile string) *Config {
	f, err := ioutil.ReadFile(cFile)

	if err != nil {
		panic(fmt.Sprintf("Failed to open config file: %v\n", err))
	}

	c := &Config{}
	err = json.Unmarshal(f, &c)

	if err != nil {
		panic(fmt.Sprintf("Could not open config file: %v\n", err))
	}

	return c
}

func logLine(ln string) (err error) {
	f, err := os.OpenFile(c.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}

	defer f.Close()

	_, err = f.WriteString(ln + "\n")
	if err != nil {
		return err
	}

	f.Sync()
	return nil
}

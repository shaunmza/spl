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
	"net"
	"net/http"
	"os"
	"os/signal"
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
	WitnessUrl  string
	PrivateKey  string
	Port        string
	AccountName string
	Currency    string
}

var client *steemgo.Client
var c *Config

func main() {
	var err error
	var cFile *string

	m := "Absolute path to config file. ie -config=/config.json"
	cFile = flag.String("config", "", m)

	mT := "Absolute path to csv file. ie -csv=/file.csv"
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
	l, err := net.ListenTCP("tcp", addr)

	if err != nil {
		if err.Error() == "listen tcp :"+c.Port+": bind: address already in use" {
			fmt.Println("Already running")
		} else {
			panic(err)
		}
		os.Exit(3)
	}
	fmt.Println("The address is:", l.Addr().String())

	t := *tFile
	importCsv(t)

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
		return "Could not decode te WIF key, please make sure you supplied the correct private key " + err.Error()
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
			if err != nil {
				log.Fatal(err)
			}

			//bigpchef	23	9/11 to 9/17 Tourneys
			tr := &transfer{To: strings.TrimSpace(record[0]), Amount: strings.TrimSpace(record[1]) + " SBD", Memo: strings.TrimSpace(record[2])}

			r := send(tr, client)
			sts := status{Message: r, Transfer: tr}
			fmt.Printf("To: %+v Amount: %+v Memo: %+v Status: %+v\n", tr.Amount, tr.To, tr.Memo, sts.Message)

		}
	}

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

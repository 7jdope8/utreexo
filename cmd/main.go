package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	bridge "github.com/mit-dci/utreexo/cmd/bridgenode"
	"github.com/mit-dci/utreexo/cmd/csn"
)

var msg = `
Usage: utreexo COMMAND [OPTION]
A dynamic hash based accumulator designed for the Bitcoin UTXO set

Commands:
  ibdsim         simulates an initial block download with ttl.testnet.txos as an input
  genproofs      generates proofs from the ttl.testnet.txos file
OPTIONS:
  testnet        configure whether the simulator should target testnet or not. Usage 'testnet=true'
`

// bit of a hack. Stdandard flag lib doesn't allow flag.Parse(os.Args[2]). You need a subcommand to do so.
var optionCmd = flag.NewFlagSet("", flag.ExitOnError)
var testnetCmd = optionCmd.Bool("testnet", false,
	"Target testnet instead of mainnet. Usage: testnet=true")

func main() {
	// check if enough arguments were given
	if len(os.Args) < 2 {
		fmt.Println(msg)
		os.Exit(1)
	}
	var ttldb, offsetfile string
	optionCmd.Parse(os.Args[2:])
	if *testnetCmd {
		ttldb = "testnetttldb"
		offsetfile = "testnetoffsetfile"
	} else {
		ttldb = "ttldb"
		offsetfile = "offsetfile"
	}
	//listen for SIGINT, SIGTERM, or SIGQUIT from the os
	sig := make(chan bool, 1)
	handleIntSig(sig)

	switch os.Args[1] {
	case "ibdsim":
		optionCmd.Parse(os.Args[2:])
		err := csn.RunIBD(*testnetCmd, offsetfile, ttldb, sig)
		if err != nil {
			panic(err)
		}
	case "genproofs":
		optionCmd.Parse(os.Args[2:])
		err := bridge.BuildProofs(*testnetCmd, ttldb, offsetfile, sig)
		if err != nil {
			panic(err)
		}
	default:
		fmt.Println(msg)
		os.Exit(0)
	}
}

func handleIntSig(sig chan bool) {
	s := make(chan os.Signal, 1)
	signal.Notify(s, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)
	go func() {
		<-s
		sig <- true
	}()
}
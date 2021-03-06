package csn

import (
	"fmt"
	"os"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/mit-dci/utreexo/accumulator"
	"github.com/mit-dci/utreexo/util"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// run IBD from block proof data
// we get the new utxo info from the same txos text file
func IBDClient(net wire.BitcoinNet,
	offsetfile string, ttldb string, sig chan bool) error {

	// Channel to alert the main loop to break when receiving a quit signal from
	// the OS
	stopGoing := make(chan bool, 1)

	// Channel to alert stopRunIBD it's ok to exit
	// Makes it wait for flushing to disk
	done := make(chan bool, 1)

	go stopRunIBD(sig, stopGoing, done)

	// Check if the blk*.dat file given is a testnet/mainnet/regtest
	// file corresponding to net
	util.CheckNet(net)

	// open database
	o := new(opt.Options)
	o.CompactionTableSizeMultiplier = 8
	o.ReadOnly = true
	lvdb, err := leveldb.OpenFile(ttldb, o)
	if err != nil {
		panic(err)
	}
	defer lvdb.Close()

	// Make neccesary directories
	util.MakePaths()

	p, height, knownTipHeight, err := initCSNState()
	if err != nil {
		panic(err)
	}

	// caching parameter. Keeps txos that are spent before than this many blocks
	lookahead := int32(1000)

	// for benchmarking
	var totalTXOAdded, totalDels int

	// blocks come in and sit in the blockQueue
	// They should come in from the network -- right now they're coming from the
	// disk but it should be the exact same thing
	ublockQueue := make(chan util.UBlock, 10)

	pFile, err := os.OpenFile(
		util.PFilePath, os.O_RDONLY, 0400)
	if err != nil {
		return err
	}

	pOffsetFile, err := os.OpenFile(
		util.POffsetFilePath, os.O_RDONLY, 0400)
	if err != nil {
		return err
	}

	// Reads blocks asynchronously from blk*.dat files, and the proof.dat, and DB
	// this will be a network reader, with the server sending the same stuff over
	go util.UBlockReader(ublockQueue, knownTipHeight, height, lookahead)

	var plustime time.Duration
	starttime := time.Now()

	// bool for stopping the below for loop
	var stop bool
	for ; height != knownTipHeight && stop != true; height++ {

		blocknproof := <-ublockQueue

		err = putBlockInPollard(blocknproof,
			&totalTXOAdded, &totalDels, plustime, &p)
		if err != nil {
			panic(err)
		}

		//if height%10000 == 0 {
		//	fmt.Printf("Block %d %s plus %.2f total %.2f proofnodes %d \n",
		//		height, newForest.Stats(),
		//		plustime.Seconds(), time.Now().Sub(starttime).Seconds(),
		//		totalProofNodes)
		//}

		if height%10000 == 0 {
			fmt.Printf("Block %d add %d del %d %s plus %.2f total %.2f \n",
				height, totalTXOAdded, totalDels, p.Stats(),
				plustime.Seconds(), time.Now().Sub(starttime).Seconds())
		}

		// Check if stopSig is no longer false
		// stop = true makes the loop exit
		select {
		case stop = <-stopGoing:
		default:
		}
	}
	pFile.Close()
	pOffsetFile.Close()

	fmt.Printf("Block %d add %d del %d %s plus %.2f total %.2f \n",
		height, totalTXOAdded, totalDels, p.Stats(),
		plustime.Seconds(), time.Now().Sub(starttime).Seconds())

	saveIBDsimData(height, p)

	fmt.Println("Done Writing")

	done <- true

	return nil
}

// Here we write proofs for all the txs.
// All the inputs are saved as 32byte sha256 hashes.
// All the outputs are saved as LeafTXO type.
func putBlockInPollard(
	ub util.UBlock,
	totalTXOAdded, totalDels *int,
	plustime time.Duration,
	p *accumulator.Pollard) error {

	plusstart := time.Now()

	inskip, outskip := util.DedupeBlock(&ub.Block)
	if !ub.ProofsProveBlock(inskip) {
		return fmt.Errorf("uData missing utxo data for block %d", ub.Height)
	}

	*totalDels += len(ub.ExtraData.AccProof.Targets) // for benchmarking

	// derive leafHashes from leafData
	if !ub.ExtraData.Verify(p.ReconstructStats()) {
		return fmt.Errorf("height %d LeafData / Proof mismatch", ub.Height)
	}
	// sort before ingestion; verify up above unsorts...
	ub.ExtraData.AccProof.SortTargets()
	// Fills in the empty(nil) nieces for verification && deletion
	err := p.IngestBatchProof(ub.ExtraData.AccProof)
	if err != nil {
		fmt.Printf("height %d ingest error\n", ub.Height)
		return err
	}

	// fmt.Printf("h %d adds %d targets %d\n",
	// 	ub.Height, len(blockAdds), len(ub.ExtraData.AccProof.Targets))

	// get hashes to add into the accumulator
	blockAdds := util.BlockToAddLeaves(
		ub.Block, nil, outskip, ub.Height)
	*totalTXOAdded += len(blockAdds) // for benchmarking

	// Utreexo tree modification. blockAdds are the added txos and
	// bp.Targets are the positions of the leaves to delete
	err = p.Modify(blockAdds, ub.ExtraData.AccProof.Targets)
	if err != nil {
		return err
	}

	donetime := time.Now()
	plustime += donetime.Sub(plusstart)

	return nil
}

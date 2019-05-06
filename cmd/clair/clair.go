package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

/* idea here:
input: load a txo / ttl file, and a memory size
output: write a bitmap of which txos to remember

how to do this:
load everything into a sorted slice (sorted by end time)
every block, remove the beginning of the slice (stuff that has died)
	- flag these as memorable; they made it to the end
add (interspersed) the new txos in the block
chop off the end of the slice (all that exceeds memory capacity)
that's all.

format of the schedule.clr file: bitmaps of 8 txos per byte.  1s mean remember, 0s mean
forget.  Not padded or anything.

format of index file: 4 bytes per block.  *Txo* position of block start, in unsigned
big endian.

So to get from a block height to a txo position, seek to 4*height in the index,
read 4 bytes, then seek to *that* /8 in the schedule file, and shift around as needed.

*/

type txoEnd struct {
	txoIdx uint32 // which utxo (in order)
	end    uint32 // when it dies (block height)
}

type sortableTxoSlice []txoEnd

func (s sortableTxoSlice) Len() int      { return len(s) }
func (s sortableTxoSlice) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s sortableTxoSlice) Less(i, j int) bool {
	return s[i].end < s[j].end
}

func (s *sortableTxoSlice) MergeSort(a sortableTxoSlice) {
	*s = append(*s, a...)
	sort.Sort(s)
}

// assumes a sorted slice.  Splits on a "end" value, returns the low slice and
// leaves the higher "end" value sequence in place
func SplitAfter(s sortableTxoSlice, h uint32) (top, bottom sortableTxoSlice) {
	for i, c := range s {
		if c.end > h {
			top = s[0:i]   // return the beginning of the slice
			bottom = s[i:] // chop that part off
			break
		}
	}
	return
}

func main() {
	fmt.Printf("clair - builds clairvoyant caching schedule\n")
	err := clairvoy()
	if err != nil {
		panic(err)
	}
	fmt.Printf("done\n")

}

func clairvoy() error {
	txofile, err := os.OpenFile("ttl.testnet3.txos", os.O_RDONLY, 0600)
	if err != nil {
		return err
	}

	clrfile, err := os.Create("schedule.clr")
	if err != nil {
		return err
	}

	// we should know how many utxos there are before starting this, and allocate
	// (truncate!? weird) that many bits (/8 for bytes)
	err = clrfile.Truncate(1000000)
	if err != nil {
		return err
	}

	// the index file will be useful later for ibdsim, if you have a block
	// height and need to know where in the clair schedule you are.
	indexFile, err := os.Create("index.clr")
	if err != nil {
		return err
	}

	defer txofile.Close()
	defer clrfile.Close()

	scanner := bufio.NewScanner(txofile)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1MB should be enough?

	maxmem := 1000

	var blockEnds sortableTxoSlice

	var clairSlice, remembers sortableTxoSlice

	var utxoCounter uint32
	var height uint32
	height = 1
	_, err = indexFile.WriteAt(U32tB(0), 0) // first 0 bytes because blocks start at 1
	if err != nil {
		return err
	}

	for scanner.Scan() {
		switch scanner.Text()[0] {
		case '-':
			// do nothing?
		case '+':

			endHeights, err := plusLine(scanner.Text())
			if err != nil {
				return err
			}
			blockEnds = make([]txoEnd, len(endHeights))
			for i, eh := range endHeights {
				blockEnds[i].txoIdx = utxoCounter
				utxoCounter++
				blockEnds[i].end = height + eh
			}

		case 'h':

			//			fmt.Printf("h %d clairslice ", height)
			//			for _, u := range clairSlice {
			//				fmt.Printf("%d:%d, ", u.txoIdx, u.end)
			//			}
			//			fmt.Printf("\n")
			txosThisBlock := uint32(len(blockEnds))

			// append & sort
			clairSlice.MergeSort(blockEnds)

			// chop off the beginning: that's the stuff that's memorable
			remembers, clairSlice = SplitAfter(clairSlice, height)

			// chop off the end, that's stuff that is forgettable
			if len(clairSlice) > maxmem {
				//				forgets := clairSlice[maxmem:]
				clairSlice = clairSlice[:maxmem]
				//				fmt.Printf("forget ")
				//				for _, f := range forgets {
				//					fmt.Printf("%d ", f.txoIdx)
				//				}
				//				fmt.Printf("\n")
			}

			// expand index file and schedule file (with lots of 0s)
			_, err := indexFile.WriteAt(
				U32tB(utxoCounter-txosThisBlock), int64(height)*4)
			if err != nil {
				return err
			}

			// writing remembers is trickier; check in
			if len(remembers) > 0 {

				fmt.Printf("h %d remember utxos ", height)
				for _, r := range remembers {
					err = assertBitInFile(r.txoIdx, clrfile)
					fmt.Printf("%d ", r.txoIdx)
				}
				fmt.Printf("\n")
			}

			//			fmt.Printf("h %d len(clairSlice) %d len(blockEnds) %d\n",
			//				height, len(clairSlice), len(blockEnds))

			height++

		default:
			panic("unknown string")
		}
	}

	return nil
}

// basically flips bit n of a big file to 1.
func assertBitInFile(txoIdx uint32, scheduleFile *os.File) error {
	offset := int64(txoIdx / 8)
	b := make([]byte, 1)
	_, err := scheduleFile.ReadAt(b, offset)
	if err != nil {
		return err
	}
	b[0] = b[0] | 1<<(7-(txoIdx%8))
	_, err = scheduleFile.WriteAt(b, offset)
	return err
}

// like the plusline in ibdsim.  Should merge with that.
// this one only returns a slice of the expiry times for the txos, but no other
// txo info.
func plusLine(s string) ([]uint32, error) {
	//	fmt.Printf("%s\n", s)
	parts := strings.Split(s[1:], ";")
	if len(parts) < 2 {
		return nil, fmt.Errorf("line %s has no ; in it", s)
	}
	postsemicolon := parts[1]

	indicatorHalves := strings.Split(postsemicolon, "x")
	ttldata := indicatorHalves[1]
	ttlascii := strings.Split(ttldata, ",")
	// the last one is always empty as there's a trailing ,
	ttlval := make([]uint32, len(ttlascii)-1)
	for i, _ := range ttlval {
		if ttlascii[i] == "s" {
			//	ttlval[i] = 0
			// 0 means don't remember it! so 1 million blocks later
			ttlval[i] = 1 << 20
			continue
		}

		val, err := strconv.Atoi(ttlascii[i])
		if err != nil {
			return nil, err
		}
		ttlval[i] = uint32(val)
	}

	txoIndicators := strings.Split(indicatorHalves[0], "z")

	numoutputs, err := strconv.Atoi(txoIndicators[0])
	if err != nil {
		return nil, err
	}
	if numoutputs != len(ttlval) {
		return nil, fmt.Errorf("%d outputs but %d ttl indicators",
			numoutputs, len(ttlval))
	}

	// numoutputs++ // for testnet3.txos

	unspend := make(map[int]bool)

	if len(txoIndicators) > 1 {
		unspendables := txoIndicators[1:]
		for _, zstring := range unspendables {
			n, err := strconv.Atoi(zstring)
			if err != nil {
				return nil, err
			}
			unspend[n] = true
		}
	}
	var ends []uint32
	for i := 0; i < numoutputs; i++ {
		if unspend[i] {
			continue
		}
		ends = append(ends, ttlval[i])
		// fmt.Printf("expire in\t%d remember %v\n", ttlval[i], addData.Remember)
	}

	return ends, nil
}

// uint32 to 4 bytes.  Always works.
func U32tB(i uint32) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.BigEndian, i)
	return buf.Bytes()
}

// 4 byte slice to uint32.  Returns ffffffff if something doesn't work.
func BtU32(b []byte) uint32 {
	if len(b) != 4 {
		fmt.Printf("Got %x to BtU32 (%d bytes)\n", b, len(b))
		return 0xffffffff
	}
	var i uint32
	buf := bytes.NewBuffer(b)
	binary.Read(buf, binary.BigEndian, &i)
	return i
}

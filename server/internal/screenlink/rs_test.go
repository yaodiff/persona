package screenlink

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestGaloisFieldArithmetic(t *testing.T) {
	for a := 1; a < 256; a++ {
		if got := gfMul[a][gfInv(byte(a))]; got != 1 {
			t.Fatalf("%d × its inverse = %d", a, got)
		}
		if gfMul[a][1] != byte(a) || gfMul[a][0] != 0 || gfMul[0][a] != 0 {
			t.Fatalf("%d × 1 or × 0 is wrong", a)
		}
	}
	// Multiplication distributes over addition (XOR).
	r := rand.New(rand.NewSource(1))
	for range 10000 {
		a, b, c := byte(r.Intn(256)), byte(r.Intn(256)), byte(r.Intn(256))
		if gfMul[a][b^c] != gfMul[a][b]^gfMul[a][c] {
			t.Fatalf("%d × (%d + %d) doesn't distribute", a, b, c)
		}
	}
}

func shardsOf(r *rand.Rand, k, n int) [][]byte {
	data := make([][]byte, k)
	for j := range data {
		data[j] = make([]byte, n)
		r.Read(data[j])
	}
	return data
}

// Any k of a block's k+m shards rebuild every data shard, for every way of losing m of them in small blocks and many
// random ways in large ones.
func TestReedSolomonRebuildsFromAnyKShards(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	check := func(k, m int, lost []int) {
		t.Helper()
		data := shardsOf(r, k, 37)
		parity, err := rsEncode(data, m)
		if err != nil {
			t.Fatal(err)
		}
		shards := append(append([][]byte{}, data...), parity...)
		for _, i := range lost {
			shards[i] = nil
		}
		if err := rsReconstruct(shards, k); err != nil {
			t.Fatalf("k=%d m=%d lost %v: %v", k, m, lost, err)
		}
		for j := 0; j < k; j++ {
			if !bytes.Equal(shards[j], data[j]) {
				t.Fatalf("k=%d m=%d lost %v: data shard %d rebuilt wrong", k, m, lost, j)
			}
		}
	}
	// Every pattern of up to m losses for small blocks.
	for k := 1; k <= 6; k++ {
		for m := 1; m <= 4; m++ {
			n := k + m
			for mask := 0; mask < 1<<n; mask++ {
				var lost []int
				for i := 0; i < n; i++ {
					if mask&(1<<i) != 0 {
						lost = append(lost, i)
					}
				}
				if len(lost) <= m {
					check(k, m, lost)
				}
			}
		}
	}
	// Random patterns for the largest blocks the protocol sends.
	for _, km := range [][2]int{{128, 1}, {128, 20}, {128, 53}, {64, 26}, {200, 56}} {
		k, m := km[0], km[1]
		for range 20 {
			check(k, m, r.Perm(k + m)[:r.Intn(m+1)])
		}
	}
}

func TestReedSolomonRefusesWhatItCannotDo(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	data := shardsOf(r, 4, 10)
	parity, _ := rsEncode(data, 2)
	shards := append(append([][]byte{}, data...), parity...)
	shards[0], shards[1], shards[5] = nil, nil, nil // three lost, two parity
	if err := rsReconstruct(shards, 4); err == nil {
		t.Fatal("rebuilt with fewer than k shards")
	}
	if _, err := rsEncode(shardsOf(r, 200, 4), 57); err == nil {
		t.Fatal("encoded more than 256 shards")
	}
	if _, err := rsEncode([][]byte{make([]byte, 3), make([]byte, 4)}, 1); err == nil {
		t.Fatal("encoded shards of different lengths")
	}
	if _, err := rsEncode(nil, 1); err == nil {
		t.Fatal("encoded an empty block")
	}
}

func BenchmarkReedSolomonEncode(b *testing.B) {
	r := rand.New(rand.NewSource(1))
	data := shardsOf(r, 128, udpFragment)
	b.SetBytes(int64(128 * udpFragment))
	for b.Loop() {
		_, _ = rsEncode(data, 20)
	}
}

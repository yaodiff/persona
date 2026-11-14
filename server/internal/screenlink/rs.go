package screenlink

import "errors"

// Reed-Solomon erasure coding for the video over UDP (#7873): a block of k data shards gets m parity shards, and any k
// of the k+m rebuild the data, so a lost packet costs no round trip. The code is systematic (the data shards go out as
// they are) and built on a Cauchy matrix over GF(2⁸), so every k×k choice of rows is invertible: parity row i, data
// column j is 1 / (x_i + y_j) with y_j = j and x_i = k + i, which keeps every x and y distinct while k + m ≤ 256.
// PersonaScreenKit implements the same code; spec/fixtures/screen-udp holds both to it.

// gfPoly is GF(2⁸)'s reducing polynomial, x⁸ + x⁴ + x³ + x² + 1, with 2 as the generator.
const gfPoly = 0x11d

var (
	gfExp [510]byte // gfExp[i] = 2^i, twice over so a sum of two logs needs no reduction
	gfLog [256]byte // gfLog[2^i] = i; gfLog[0] is unused
	gfMul [256][256]byte
)

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		gfExp[i], gfExp[i+255] = byte(x), byte(x)
		gfLog[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= gfPoly
		}
	}
	for a := 1; a < 256; a++ {
		for b := 1; b < 256; b++ {
			gfMul[a][b] = gfExp[int(gfLog[a])+int(gfLog[b])]
		}
	}
}

func gfInv(a byte) byte { return gfExp[255-int(gfLog[a])] }

// errRS is a block that can't be encoded or rebuilt as given.
var errRS = errors.New("screenlink: reed-solomon")

// rsCoefficient is the parity row i, data column j entry of a block with k data shards.
func rsCoefficient(k, i, j int) byte { return gfInv(byte(k+i) ^ byte(j)) }

// rsEncode returns m parity shards for data, k shards of one length.
func rsEncode(data [][]byte, m int) ([][]byte, error) {
	k := len(data)
	if k == 0 || m < 0 || k+m > 256 {
		return nil, errRS
	}
	n := len(data[0])
	for _, d := range data {
		if len(d) != n {
			return nil, errRS
		}
	}
	parity := make([][]byte, m)
	for i := range parity {
		p := make([]byte, n)
		for j, d := range data {
			mul := &gfMul[rsCoefficient(k, i, j)]
			for b, v := range d {
				p[b] ^= mul[v]
			}
		}
		parity[i] = p
	}
	return parity, nil
}

// rsReconstruct fills in the missing data shards of a block: shards holds k data then m parity shards, nil where
// missing, every present one of the same length. It needs at least k present; parity shards are left as they are.
func rsReconstruct(shards [][]byte, k int) error {
	m := len(shards) - k
	if k == 0 || m < 0 || len(shards) > 256 {
		return errRS
	}
	var missing []int
	for j := 0; j < k; j++ {
		if shards[j] == nil {
			missing = append(missing, j)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	// The k rows used: every data shard present, then parity shards for the rest.
	rows := make([]int, 0, k)
	n := -1
	for i, s := range shards {
		if s == nil || len(rows) == k {
			continue
		}
		if n >= 0 && len(s) != n {
			return errRS
		}
		n = len(s)
		rows = append(rows, i)
	}
	if len(rows) < k {
		return errRS
	}
	// The generator's rows for them, inverted: data row j of the inverse rebuilds data shard j from the rows used.
	a := make([][]byte, k)
	for r, row := range rows {
		a[r] = make([]byte, k)
		if row < k {
			a[r][row] = 1
		} else {
			for j := 0; j < k; j++ {
				a[r][j] = rsCoefficient(k, row-k, j)
			}
		}
	}
	inv, err := gfInvert(a)
	if err != nil {
		return err
	}
	for _, j := range missing {
		out := make([]byte, n)
		for r, row := range rows {
			c := inv[j][r]
			if c == 0 {
				continue
			}
			mul := &gfMul[c]
			for b, v := range shards[row] {
				out[b] ^= mul[v]
			}
		}
		shards[j] = out
	}
	return nil
}

// gfInvert inverts a square matrix over GF(2⁸) by Gauss-Jordan elimination.
func gfInvert(a [][]byte) ([][]byte, error) {
	k := len(a)
	m := make([][]byte, k)
	for r := range a {
		m[r] = make([]byte, 2*k)
		copy(m[r], a[r])
		m[r][k+r] = 1
	}
	for c := 0; c < k; c++ {
		p := c
		for p < k && m[p][c] == 0 {
			p++
		}
		if p == k {
			return nil, errRS // singular: never for rows of a systematic Cauchy generator
		}
		m[c], m[p] = m[p], m[c]
		if v := m[c][c]; v != 1 {
			mul := &gfMul[gfInv(v)]
			for x := range m[c] {
				m[c][x] = mul[m[c][x]]
			}
		}
		for r := 0; r < k; r++ {
			if r == c || m[r][c] == 0 {
				continue
			}
			mul := &gfMul[m[r][c]]
			for x := range m[r] {
				m[r][x] ^= mul[m[c][x]]
			}
		}
	}
	out := make([][]byte, k)
	for r := range m {
		out[r] = m[r][k:]
	}
	return out, nil
}

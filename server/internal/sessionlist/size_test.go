package sessionlist

import (
	"testing"
	"unsafe"
)

// A hub pass reads one machine's rows as a single []Item, and the allocation budgets assume that this 150-row batch
// stays in Go's largest small-object size class. Keep this beside Item so an added field fails here, before the less
// direct hub budgets fail. The batch-size check is valid on 32-bit and 64-bit architectures; it pins no struct size.
func TestItemBatchFitsItsSizeClass(t *testing.T) {
	const (
		rows       = 150
		batchClass = 32 << 10
	)
	item := unsafe.Sizeof(Item{})
	if batch := rows * item; batch > batchClass {
		t.Errorf("Item is %d bytes; %d rows take %d bytes, past the %d-byte size class (the batch takes whole pages "+
			"instead): put sparse attributes behind the existing optional pointer", item, rows, batch, batchClass)
	}
}

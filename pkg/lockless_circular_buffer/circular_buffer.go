package lockless_circular_buffer

import (
	"runtime"
	"time"
	"unsafe"

	"go.uber.org/atomic"
)

type CircularBuffer[T any] struct {
	buffer []T
	head   *atomic.Uint32
	tail   *atomic.Uint32
	mask   uint32
	size   uint32

	// todo(anunaym14): arch-aware padding to avoid false sharing
	// _padding [x]byte
}

func NewCircularBuffer[T any](capacity uint32) *CircularBuffer[T] {
	// Ensure capacity is a power of 2 and at least 1
	// todo(anunaym14): cleanup
	if capacity == 0 {
		capacity = 1
	} else if (capacity & (capacity - 1)) != 0 {
		capacity--
		capacity |= capacity >> 1
		capacity |= capacity >> 2
		capacity |= capacity >> 4
		capacity |= capacity >> 8
		capacity |= capacity >> 16
		capacity++
	}

	return &CircularBuffer[T]{
		buffer: make([]T, capacity),
		head:   atomic.NewUint32(0),
		tail:   atomic.NewUint32(0),
		mask:   capacity - 1,
		size:   capacity,
	}
}

func (cb *CircularBuffer[T]) Push(item T) {
	backoffCounter := 0
	backoffMax := 32

	for {
		tail := cb.tail.Load()
		head := cb.head.Load()

		nextTail := (tail + 1) & cb.mask
		if nextTail == head {
			if backoffCounter < backoffMax {
				backoffCounter++
				continue
			}
			runtime.Gosched()
			continue
		}

		if cb.tail.CompareAndSwap(tail, nextTail) {
			cb.buffer[tail] = item
			return
		}

		if backoffCounter > 0 {
			backoffCounter--
		}
	}
}

func (cb *CircularBuffer[T]) TryPush(item T) bool {
	const maxAttempts = 5

	for i := 0; i < maxAttempts; i++ {
		tail := cb.tail.Load()
		head := cb.head.Load()

		nextTail := (tail + 1) & cb.mask
		if nextTail == head {
			return false
		}

		if cb.tail.CompareAndSwap(tail, nextTail) {
			cb.buffer[tail] = item
			return true
		}

		if i > 1 {
			runtime.Gosched()
		}
	}
	return false
}

func (cb *CircularBuffer[T]) PushTimeout(item T, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		tail := cb.tail.Load()
		head := cb.head.Load()

		nextTail := (tail + 1) & cb.mask
		if nextTail == head {
			runtime.Gosched()
			continue
		}

		if cb.tail.CompareAndSwap(tail, nextTail) {
			cb.buffer[tail] = item
			return true
		}
	}

	return false
}

func (cb *CircularBuffer[T]) Pop() (T, bool) {
	var zero T
	const maxAttempts = 5

	for i := 0; i < maxAttempts; i++ {
		head := cb.head.Load()
		tail := cb.tail.Load()

		if head == tail {
			return zero, false
		}

		nextHead := (head + 1) & cb.mask
		if cb.head.CompareAndSwap(head, nextHead) {
			item := cb.buffer[head]
			return item, true
		}

		if i > 1 {
			runtime.Gosched()
		}
	}
	return zero, false
}

func (cb *CircularBuffer[T]) PushBatch(items []T) int {
	if len(items) == 0 {
		return 0
	}

	head := cb.head.Load()
	tail := cb.tail.Load()

	var availableSpace uint32
	if head <= tail {
		availableSpace = cb.size - (tail - head) - 1
	} else {
		availableSpace = head - tail - 1
	}

	batchSize := uint32(len(items))
	if batchSize > availableSpace {
		batchSize = availableSpace
	}

	if batchSize == 0 {
		return 0
	}

	pushed := uint32(0)
	for pushed < batchSize {
		tail = cb.tail.Load()
		head = cb.head.Load()

		if head <= tail {
			availableSpace = cb.size - (tail - head) - 1
		} else {
			availableSpace = head - tail - 1
		}

		if availableSpace == 0 {
			break
		}

		currentBatchSize := batchSize - pushed
		if currentBatchSize > availableSpace {
			currentBatchSize = availableSpace
		}

		newTail := (tail + currentBatchSize) & cb.mask

		if cb.tail.CompareAndSwap(tail, newTail) {
			for i := uint32(0); i < currentBatchSize; i++ {
				slotIndex := (tail + i) & cb.mask
				cb.buffer[slotIndex] = items[pushed+i]
			}
			pushed += currentBatchSize
		}
	}

	return int(pushed)
}

func (cb *CircularBuffer[T]) PushBatchBlocking(items []T) {
	remaining := items
	for len(remaining) > 0 {
		pushed := cb.PushBatch(remaining)
		if pushed == 0 {
			runtime.Gosched()
			continue
		}
		remaining = remaining[pushed:]
	}
}

func (cb *CircularBuffer[T]) PopBatch(maxItems int) (int, []T) {
	if maxItems <= 0 {
		return 0, nil
	}

	head := cb.head.Load()
	tail := cb.tail.Load()

	var availableItems uint32
	if tail >= head {
		availableItems = tail - head
	} else {
		availableItems = cb.size - (head - tail)
	}

	batchSize := uint32(maxItems)
	if batchSize > availableItems {
		batchSize = availableItems
	}

	if batchSize == 0 {
		return 0, nil
	}

	result := make([]T, 0, batchSize)
	popped := uint32(0)

	for popped < batchSize {
		head = cb.head.Load()
		tail = cb.tail.Load()

		if tail >= head {
			availableItems = tail - head
		} else {
			availableItems = cb.size - (head - tail)
		}

		if availableItems == 0 {
			break
		}

		currentBatchSize := batchSize - popped
		if currentBatchSize > availableItems {
			currentBatchSize = availableItems
		}

		newHead := (head + currentBatchSize) & cb.mask

		if cb.head.CompareAndSwap(head, newHead) {
			for i := uint32(0); i < currentBatchSize; i++ {
				slotIndex := (head + i) & cb.mask
				result = append(result, cb.buffer[slotIndex])
			}
			popped += currentBatchSize
		}
	}

	return int(popped), result
}

//go:noinline
func prefetch(addr unsafe.Pointer) {
	_ = addr
}

func (cb *CircularBuffer[T]) AddPreloaded(items []T) int {
	if len(items) == 0 {
		return 0
	}

	prefetch(unsafe.Pointer(cb.head))
	prefetch(unsafe.Pointer(cb.tail))

	return cb.PushBatch(items)
}

func (cb *CircularBuffer[T]) Size() uint32 {
	head := cb.head.Load()
	tail := cb.tail.Load()

	if head == tail {
		return 0
	}

	if tail >= head {
		return tail - head
	}

	return cb.size - (head - tail)
}

func (cb *CircularBuffer[T]) Capacity() uint32 {
	return cb.size
}

func (cb *CircularBuffer[T]) IsEmpty() bool {
	return cb.head.Load() == cb.tail.Load()
}

func (cb *CircularBuffer[T]) IsFull() bool {
	head := cb.head.Load()
	tail := cb.tail.Load()

	return ((tail + 1) & cb.mask) == head
}

func (cb *CircularBuffer[T]) Clear() {
	tail := cb.tail.Load()
	for {
		head := cb.head.Load()
		if head == tail || cb.head.CompareAndSwap(head, tail) {
			break
		}
		runtime.Gosched()
	}
}

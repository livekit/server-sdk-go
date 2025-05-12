package lockless_circular_buffer

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCircularBufferBasicOperations(t *testing.T) {
	t.Run("empty buffer", func(t *testing.T) {
		cb := NewCircularBuffer[int](16)
		require.True(t, cb.IsEmpty())
		require.False(t, cb.IsFull())
		require.Equal(t, uint32(0), cb.Size())
		_, ok := cb.Pop()
		require.False(t, ok)
	})

	t.Run("push and pop", func(t *testing.T) {
		cb := NewCircularBuffer[string](16)
		cb.Push("test")
		require.False(t, cb.IsEmpty())
		require.Equal(t, uint32(1), cb.Size())
		item, ok := cb.Pop()
		require.True(t, ok)
		require.Equal(t, "test", item)
		require.True(t, cb.IsEmpty())
		require.Equal(t, uint32(0), cb.Size())
	})

	t.Run("custom type", func(t *testing.T) {
		type customType struct {
			ID   int
			Name string
		}

		cb := NewCircularBuffer[customType](16)
		item := customType{ID: 1, Name: "test"}
		cb.Push(item)
		popped, ok := cb.Pop()
		require.True(t, ok)
		require.Equal(t, item, popped)
	})

	t.Run("full buffer", func(t *testing.T) {
		cb := NewCircularBuffer[int](4)

		for i := 0; i < 3; i++ {
			cb.Push(i)
		}

		require.False(t, cb.IsEmpty())
		require.True(t, cb.IsFull())

		success := cb.TryPush(4)
		require.False(t, success)

		item, ok := cb.Pop()
		require.True(t, ok)
		require.Equal(t, 0, item)
		require.False(t, cb.IsFull())

		success = cb.TryPush(4)
		require.True(t, success)
	})

	t.Run("push timeout", func(t *testing.T) {
		cb := NewCircularBuffer[int](4)

		for i := 0; i < 3; i++ {
			cb.Push(i)
		}

		require.True(t, cb.IsFull())

		success := cb.PushTimeout(4, 10*time.Millisecond)
		require.False(t, success)

		_, _ = cb.Pop()

		success = cb.PushTimeout(4, 10*time.Millisecond)
		require.True(t, success)
	})
}

func TestCircularBufferBatchOperations(t *testing.T) {
	t.Run("push batch non-blocking", func(t *testing.T) {
		cb := NewCircularBuffer[int](8)
		items := []int{1, 2, 3, 4, 5}

		pushed := cb.PushBatch(items)
		require.Equal(t, 5, pushed)

		pushed = cb.PushBatch([]int{6, 7, 8})
		require.Equal(t, 2, pushed)

		require.True(t, cb.IsFull())

		pushed = cb.PushBatch([]int{9, 10})
		require.Equal(t, 0, pushed)

		for i := 1; i <= 7; i++ {
			item, ok := cb.Pop()
			require.True(t, ok)
			require.Equal(t, i, item)
		}

		require.True(t, cb.IsEmpty())
	})

	t.Run("push batch blocking", func(t *testing.T) {
		cb := NewCircularBuffer[int](4)
		items := []int{1, 2, 3}

		cb.PushBatchBlocking(items)

		require.Equal(t, uint32(3), cb.Size())

		for i := 1; i <= 3; i++ {
			item, ok := cb.Pop()
			require.True(t, ok)
			require.Equal(t, i, item)
		}
	})

	t.Run("pop batch", func(t *testing.T) {
		cb := NewCircularBuffer[int](16)

		for i := 1; i <= 10; i++ {
			cb.Push(i)
		}

		count, items := cb.PopBatch(5)
		require.Equal(t, 5, count)
		require.Equal(t, 5, len(items))
		for i := 0; i < 5; i++ {
			require.Equal(t, i+1, items[i])
		}

		count, items = cb.PopBatch(10)
		require.Equal(t, 5, count)
		require.Equal(t, 5, len(items))
		for i := 0; i < 5; i++ {
			require.Equal(t, i+6, items[i])
		}

		require.True(t, cb.IsEmpty())

		count, items = cb.PopBatch(5)
		require.Equal(t, 0, count)
		require.Equal(t, 0, len(items))
	})
}

func TestCircularBufferConcurrentOperations(t *testing.T) {
	t.Run("concurrent push and pop", func(t *testing.T) {
		cb := NewCircularBuffer[int](1024)
		const itemCount = 1000
		var wg sync.WaitGroup

		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(offset int) {
				defer wg.Done()
				for j := 0; j < itemCount/4; j++ {
					value := offset*itemCount/4 + j
					cb.Push(value)
				}
			}(i)
		}

		results := make(map[int]struct{})
		var mu sync.Mutex

		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					count, items := cb.PopBatch(10)
					if count > 0 {
						mu.Lock()
						for _, item := range items {
							results[item] = struct{}{}
						}
						mu.Unlock()
					} else {
						if len(results) >= itemCount {
							break
						}
						time.Sleep(1 * time.Millisecond)
					}
				}
			}()
		}

		wg.Wait()

		require.Equal(t, itemCount, len(results))
		for i := 0; i < itemCount; i++ {
			_, ok := results[i]
			require.True(t, ok, "Item %d was not found in results", i)
		}
	})

	t.Run("concurrent batch operations", func(t *testing.T) {
		cb := NewCircularBuffer[int](1024)
		const batchCount = 20
		const batchSize = 50
		var wg sync.WaitGroup

		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(offset int) {
				defer wg.Done()
				for j := 0; j < batchCount/4; j++ {
					batch := make([]int, batchSize)
					for k := 0; k < batchSize; k++ {
						batch[k] = offset*(batchCount/4)*batchSize + j*batchSize + k
					}
					cb.PushBatchBlocking(batch)
				}
			}(i)
		}

		results := make(map[int]struct{})
		var mu sync.Mutex
		totalItems := batchCount * batchSize

		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					count, items := cb.PopBatch(20)
					if count > 0 {
						mu.Lock()
						for _, item := range items {
							results[item] = struct{}{}
						}
						mu.Unlock()
					} else {
						mu.Lock()
						resultCount := len(results)
						mu.Unlock()
						if resultCount >= totalItems {
							break
						}
						time.Sleep(1 * time.Millisecond)
					}
				}
			}()
		}

		wg.Wait()

		require.Equal(t, totalItems, len(results))
		for i := 0; i < totalItems; i++ {
			_, ok := results[i]
			require.True(t, ok, "Item %d was not found in results", i)
		}
	})
}

func BenchmarkCircularBuffer(b *testing.B) {
	b.Run("single push-pop", func(b *testing.B) {
		cb := NewCircularBuffer[int](1024)
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			cb.Push(i)
			_, _ = cb.Pop()
		}
	})

	b.Run("batch push-pop-small", func(b *testing.B) {
		cb := NewCircularBuffer[int](1024)
		batchSize := 10
		items := make([]int, batchSize)
		for i := 0; i < batchSize; i++ {
			items[i] = i
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			pushed := cb.PushBatch(items)
			_, popped := cb.PopBatch(pushed)
			if len(popped) != pushed {
				b.Fatalf("pushed %d items but popped %d", pushed, len(popped))
			}
		}
	})

	b.Run("batch push-pop-large", func(b *testing.B) {
		cb := NewCircularBuffer[int](1024)
		batchSize := 100
		items := make([]int, batchSize)
		for i := 0; i < batchSize; i++ {
			items[i] = i
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			pushed := cb.PushBatch(items)
			_, popped := cb.PopBatch(pushed)
			if len(popped) != pushed {
				b.Fatalf("pushed %d items but popped %d", pushed, len(popped))
			}
		}
	})

	b.Run("concurrent-small", func(b *testing.B) {
		cb := NewCircularBuffer[int](1024)
		const numGoroutines = 4
		const itemsPerGoroutine = 100

		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			b.StopTimer()

			for !cb.IsEmpty() {
				cb.Pop()
			}

			var wg sync.WaitGroup

			for j := 0; j < numGoroutines; j++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()
					for k := 0; k < itemsPerGoroutine; k++ {
						cb.Push(id*itemsPerGoroutine + k)
					}
				}(j)
			}

			for j := 0; j < numGoroutines; j++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					remainingItems := itemsPerGoroutine
					for remainingItems > 0 {
						if item, ok := cb.Pop(); ok {
							_ = item
							remainingItems--
						}
					}
				}()
			}

			b.StartTimer()
			wg.Wait()
		}
	})

	b.Run("concurrent-batch", func(b *testing.B) {
		cb := NewCircularBuffer[int](1024)
		const numGoroutines = 4
		const batchesPerGoroutine = 20
		const batchSize = 10

		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			b.StopTimer()

			for !cb.IsEmpty() {
				cb.Pop()
			}

			var wg sync.WaitGroup

			items := make([]int, batchSize)
			for j := 0; j < batchSize; j++ {
				items[j] = j
			}

			for j := 0; j < numGoroutines; j++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for k := 0; k < batchesPerGoroutine; k++ {
						cb.PushBatch(items)
					}
				}()
			}

			for j := 0; j < numGoroutines; j++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					remainingBatches := batchesPerGoroutine
					for remainingBatches > 0 {
						count, _ := cb.PopBatch(batchSize)
						if count > 0 {
							remainingBatches--
						}
					}
				}()
			}

			b.StartTimer()
			wg.Wait()
		}
	})

	b.Run("pop-multi", func(b *testing.B) {
		cb := NewCircularBuffer[int](1024)
		batchSize := 10

		for i := 0; i < 1000; i++ {
			cb.Push(i)
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			count, _ := cb.PopBatch(batchSize)
			if count == 0 {
				b.StopTimer()
				for j := 0; j < 1000; j++ {
					cb.Push(j)
				}
				b.StartTimer()
				_, _ = cb.PopBatch(batchSize)
			}
		}
	})

	b.Run("preloaded-push", func(b *testing.B) {
		cb := NewCircularBuffer[int](1024)
		batchSize := 100
		items := make([]int, batchSize)
		for i := 0; i < batchSize; i++ {
			items[i] = i
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			pushed := cb.AddPreloaded(items)
			_, popped := cb.PopBatch(pushed)
			if len(popped) != pushed {
				b.Fatalf("pushed %d items but popped %d", pushed, len(popped))
			}
		}
	})
}

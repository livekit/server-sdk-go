package lksdk

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkUtf8String(t *testing.T) {
	t.Run("Empty string", func(t *testing.T) {
		chunks := chunkUtf8String("")
		if len(chunks) != 0 {
			t.Errorf("Expected 0 chunks for empty string, got %d", len(chunks))
		}
	})

	t.Run("String shorter than chunk size", func(t *testing.T) {
		testString := strings.Repeat("a", 1000)
		chunks := chunkUtf8String(testString)

		if len(chunks) != 1 {
			t.Errorf("Expected 1 chunk, got %d", len(chunks))
		}
		if string(chunks[0]) != testString {
			t.Errorf("Chunk content doesn't match original string")
		}
	})

	t.Run("String exactly at chunk size", func(t *testing.T) {
		testString := strings.Repeat("a", STREAM_CHUNK_SIZE)
		chunks := chunkUtf8String(testString)

		if len(chunks) != 1 {
			t.Errorf("Expected 1 chunk, got %d", len(chunks))
		}
		if string(chunks[0]) != testString {
			t.Errorf("Chunk content doesn't match original string")
		}
	})

	t.Run("ASCII string longer than chunk size", func(t *testing.T) {
		testString := strings.Repeat("a", STREAM_CHUNK_SIZE*2+100)
		chunks := chunkUtf8String(testString)

		if len(chunks) != 3 {
			t.Errorf("Expected 3 chunks, got %d", len(chunks))
		}

		// Reconstruct the original string and check
		var reconstructed []byte
		for _, chunk := range chunks {
			reconstructed = append(reconstructed, chunk...)
		}

		if string(reconstructed) != testString {
			t.Errorf("Reconstructed string doesn't match original")
		}

		// First two chunks should be exactly STREAM_CHUNK_SIZE
		if len(chunks[0]) != STREAM_CHUNK_SIZE {
			t.Errorf("Expected first chunk to be %d bytes, got %d", STREAM_CHUNK_SIZE, len(chunks[0]))
		}
		if len(chunks[1]) != STREAM_CHUNK_SIZE {
			t.Errorf("Expected second chunk to be %d bytes, got %d", STREAM_CHUNK_SIZE, len(chunks[1]))
		}
	})

	t.Run("UTF-8 multi-byte characters at chunk boundaries", func(t *testing.T) {
		// Create a string with multi-byte UTF-8 characters
		// "你好" (ni hao) is 6 bytes (3 bytes per character)
		multiBytePrefix := strings.Repeat("你好", 1000)
		// Pad with ASCII to get close to the chunk boundary
		paddingSize := STREAM_CHUNK_SIZE - (len(multiBytePrefix) % STREAM_CHUNK_SIZE) - 2
		if paddingSize < 0 {
			paddingSize += STREAM_CHUNK_SIZE
		}
		padding := strings.Repeat("a", paddingSize)

		// Add a multi-byte character at the end to test boundary handling
		multiByteSuffix := "您"
		testString := multiBytePrefix + padding + multiByteSuffix

		chunks := chunkUtf8String(testString)

		if len(chunks) != 2 {
			t.Errorf("Expected 2 chunks, got %d", len(chunks))
		}

		// Validate each chunk is valid UTF-8
		for i, chunk := range chunks {
			if !utf8.Valid(chunk) {
				t.Errorf("Chunk %d is not valid UTF-8", i)
			}

			if i == 0 {
				if len(chunk) != len(multiBytePrefix+padding) {
					t.Errorf("Expected first chunk to be %d bytes, got %d", len(multiBytePrefix+padding), len(chunk))
				}
			} else if i == 1 {
				if len(chunk) != len(multiByteSuffix) {
					t.Errorf("Expected second chunk to be %d bytes, got %d", len(multiByteSuffix), len(chunk))
				}
			}
		}

		// Reconstruct and verify the original string
		var reconstructed []byte
		for _, chunk := range chunks {
			reconstructed = append(reconstructed, chunk...)
		}

		if string(reconstructed) != testString {
			t.Errorf("Reconstructed string doesn't match original")
		}
	})

	t.Run("String with various UTF-8 characters", func(t *testing.T) {
		// Mix of 1, 2, 3, and 4 byte UTF-8 characters
		// With a pattern that doesn't divide evenly into the chunk size
		testChars := []string{
			"a", // 1 byte (ASCII)
			"é", // 2 bytes (Latin-1 Supplement)
			"你", // 3 bytes (CJK)
			"🚀", // 4 bytes (Emoji)
			"世", // 3 bytes (another CJK character)
		}

		// Create a large string by repeating these characters
		var builder strings.Builder
		for i := 0; i < STREAM_CHUNK_SIZE; i++ {
			builder.WriteString(testChars[i%len(testChars)])
		}
		testString := builder.String()

		chunks := chunkUtf8String(testString)

		// Total string size calculation:
		// - We add STREAM_CHUNK_SIZE characters to the string (15,000 characters)
		// - These characters follow a repeating pattern of 5 different characters
		// - Each 5-character pattern consists of:
		//   * "a"  - 1 byte
		//   * "é"  - 2 bytes
		//   * "你" - 3 bytes
		//   * "🚀" - 4 bytes
		//   * "世" - 3 bytes
		// - Total: 13 bytes per 5-character pattern
		// - We have 15,000 / 5 = 3,000 complete patterns
		// - 3,000 patterns × 13 bytes = 39,000 bytes total string size

		// Expected chunk sizes based on the algorithm behavior:
		// - Each full pattern is exactly 13 bytes (a + é + 你 + 🚀 + 世 = 1+2+3+4+3 = 13 bytes)
		// - Since 13 doesn't divide evenly into 15,000, chunks need to adjust to respect UTF-8 boundaries
		// - We can calculate the exact chunk sizes by examining how patterns fit into chunks:
		//
		// - Chunk 1 (14999 bytes): 1,153 complete patterns = 14,989 bytes
		//      Then we can accomodate additional 10 bytes before crossing the max chunk size (1 + 2 + 3 + 4)
		// - Chunk 2 (14998 bytes): Another 1,153 patterns = 14,989 bytes
		//      Then we can accomodate additional 9 bytes before crossing the max chunk size (3 + 1 + 2 + 3)
		// - Chunk 3 (9003 bytes): The remaining bytes
		//
		// - Total: 39,000 bytes (14,999 + 14,998 + 9003)
		expectedSizes := []int{14999, 14998, 9003}
		for i, expectedSize := range expectedSizes {
			if i < len(chunks) && len(chunks[i]) != expectedSize {
				t.Errorf("Chunk %d: expected size %d bytes, got %d bytes", i, expectedSize, len(chunks[i]))
			}
		}

		if len(chunks) != len(expectedSizes) {
			t.Errorf("Expected %d chunks, got %d", len(expectedSizes), len(chunks))
		}

		// Verify each chunk is valid UTF-8
		for i, chunk := range chunks {
			if !utf8.Valid(chunk) {
				t.Errorf("Chunk %d is not valid UTF-8", i)
			}
		}

		// Reconstructed string should match the original
		var reconstructed []byte
		for _, chunk := range chunks {
			reconstructed = append(reconstructed, chunk...)
		}

		if string(reconstructed) != testString {
			t.Errorf("Reconstructed string doesn't match original")
		}
	})

	t.Run("UTF-8 boundary detection", func(t *testing.T) {
		// Create a string where a multi-byte character crosses the STREAM_CHUNK_SIZE boundary
		// The Chinese character "好" takes 3 bytes
		// We want to position it so the STREAM_CHUNK_SIZE index falls on the last byte or middle of the character

		// Create a prefix that puts the start of the Chinese character right before the STREAM_CHUNK_SIZE boundary
		prefix := strings.Repeat("a", STREAM_CHUNK_SIZE-2)

		// Now the 3-byte character "好" will span positions:
		// STREAM_CHUNK_SIZE-2, STREAM_CHUNK_SIZE-1, and STREAM_CHUNK_SIZE
		testString := prefix + "好" + "additional content"

		chunks := chunkUtf8String(testString)

		// We expect the function to detect that the byte at position STREAM_CHUNK_SIZE is a continuation byte
		// and back up to the start of the character
		if len(chunks) != 2 {
			t.Fatalf("Expected 2 chunks, got %d", len(chunks))
		}

		// The first chunk should end before the Chinese character
		if len(chunks[0]) != len(prefix) {
			t.Errorf("Expected first chunk to be %d bytes, got %d", len(prefix), len(chunks[0]))
		}

		// The second chunk should start with the character that would have been split
		expectedSecondChunk := "好" + "additional content"
		if string(chunks[1]) != expectedSecondChunk {
			t.Errorf("Second chunk doesn't match expected content: %s vs %s",
				string(chunks[1]), expectedSecondChunk)
		}

		// Verify all chunks are valid UTF-8
		validateUtf8Chunks(t, chunks)

		// Verify the reconstructed string matches the original
		var reconstructed []byte
		for _, chunk := range chunks {
			reconstructed = append(reconstructed, chunk...)
		}
		if string(reconstructed) != testString {
			t.Errorf("Reconstructed string doesn't match original")
		}
	})

	// Add a test with 4-byte UTF-8 characters (emojis) at the boundary
	t.Run("UTF-8 boundary with 4-byte characters", func(t *testing.T) {
		// Create a string where a 4-byte character crosses the STREAM_CHUNK_SIZE boundary
		// Position the emoji so the STREAM_CHUNK_SIZE index falls on one of its continuation bytes

		// Create a prefix that puts the start of the emoji right before the STREAM_CHUNK_SIZE boundary
		prefix := strings.Repeat("a", STREAM_CHUNK_SIZE-3)

		// Now the 4-byte character "🚀" will span positions:
		// STREAM_CHUNK_SIZE-3, STREAM_CHUNK_SIZE-2, STREAM_CHUNK_SIZE-1, and STREAM_CHUNK_SIZE
		testString := prefix + "🚀" + "more content"

		chunks := chunkUtf8String(testString)

		// We expect the function to detect that the byte at position STREAM_CHUNK_SIZE is a continuation byte
		// and back up to the start of the character
		if len(chunks) != 2 {
			t.Fatalf("Expected 2 chunks, got %d", len(chunks))
		}

		// The first chunk should end before the emoji
		if len(chunks[0]) != len(prefix) {
			t.Errorf("Expected first chunk to be %d bytes, got %d", len(prefix), len(chunks[0]))
		}

		// The second chunk should start with the emoji that would have been split
		expectedSecondChunk := "🚀" + "more content"
		if string(chunks[1]) != expectedSecondChunk {
			t.Errorf("Second chunk doesn't match expected content")
		}

		// Verify all chunks are valid UTF-8
		validateUtf8Chunks(t, chunks)
	})

	// Test a pathological case where a UTF-8 sequence is exactly at the chunk boundary
	t.Run("UTF-8 sequence exactly at chunk boundary", func(t *testing.T) {
		// Create a string that's exactly STREAM_CHUNK_SIZE - 1
		prefix := strings.Repeat("a", STREAM_CHUNK_SIZE-1)

		// Add a 2-byte character to cross the boundary
		// "é" takes 2 bytes in UTF-8
		testString := prefix + "é" + strings.Repeat("a", STREAM_CHUNK_SIZE)

		chunks := chunkUtf8String(testString)

		if len(chunks) != 3 {
			t.Fatalf("Expected 3 chunks, got %d", len(chunks))
		}

		expectedSizes := []int{STREAM_CHUNK_SIZE - 1, STREAM_CHUNK_SIZE, 2}
		for i, expectedSize := range expectedSizes {
			if len(chunks[i]) != expectedSize {
				t.Errorf("Chunk %d expected size %d, got %d", i, expectedSize, len(chunks[i]))
			}
		}

		// Verify all chunks are valid UTF-8
		validateUtf8Chunks(t, chunks)

		// Verify the reconstructed string matches the original
		var reconstructed []byte
		for _, chunk := range chunks {
			reconstructed = append(reconstructed, chunk...)
		}
		if string(reconstructed) != testString {
			t.Errorf("Reconstructed string doesn't match original")
		}
	})
}

// Helper function to validate that chunks properly handle UTF-8 boundaries
func validateUtf8Chunks(t *testing.T, chunks [][]byte) {
	for i, chunk := range chunks {
		if !utf8.Valid(chunk) {
			t.Errorf("Chunk %d contains invalid UTF-8", i)
		}
	}
}

func TestChunkBytes(t *testing.T) {
	t.Run("Empty byte slice", func(t *testing.T) {
		chunks := chunkBytes([]byte{})
		if len(chunks) != 0 {
			t.Errorf("Expected 0 chunks for empty byte slice, got %d", len(chunks))
		}
	})

	t.Run("Byte slice shorter than chunk size", func(t *testing.T) {
		testData := bytes.Repeat([]byte{1, 2, 3}, 1000)
		chunks := chunkBytes(testData)

		if len(chunks) != 1 {
			t.Errorf("Expected 1 chunk, got %d", len(chunks))
		}
		if !bytes.Equal(chunks[0], testData) {
			t.Errorf("Chunk content doesn't match original data")
		}
	})

	t.Run("Byte slice exactly at chunk size", func(t *testing.T) {
		testData := bytes.Repeat([]byte{1}, STREAM_CHUNK_SIZE)
		chunks := chunkBytes(testData)

		if len(chunks) != 1 {
			t.Errorf("Expected 1 chunk, got %d", len(chunks))
		}
		if !bytes.Equal(chunks[0], testData) {
			t.Errorf("Chunk content doesn't match original data")
		}
	})

	t.Run("Byte slice longer than chunk size", func(t *testing.T) {
		testData := bytes.Repeat([]byte{1, 2, 3, 4}, STREAM_CHUNK_SIZE)
		// Add some extra bytes
		testData = append(testData, []byte{5, 6, 7, 8, 9, 10}...)

		chunks := chunkBytes(testData)

		expectedChunks := (len(testData) + STREAM_CHUNK_SIZE - 1) / STREAM_CHUNK_SIZE
		if len(chunks) != expectedChunks {
			t.Errorf("Expected %d chunks, got %d", expectedChunks, len(chunks))
		}

		// Reconstruct the original data and check
		var reconstructed []byte
		for _, chunk := range chunks {
			reconstructed = append(reconstructed, chunk...)
		}

		if !bytes.Equal(reconstructed, testData) {
			t.Errorf("Reconstructed data doesn't match original")
		}

		// Check sizes of chunks
		for i, chunk := range chunks {
			if i < len(chunks)-1 {
				// All chunks except possibly the last should be exactly STREAM_CHUNK_SIZE
				if len(chunk) != STREAM_CHUNK_SIZE {
					t.Errorf("Chunk %d expected to be %d bytes, got %d", i, STREAM_CHUNK_SIZE, len(chunk))
				}
			} else if i == len(chunks)-1 && len(testData)%STREAM_CHUNK_SIZE != 0 {
				// Last chunk should be the remainder
				expectedSize := len(testData) % STREAM_CHUNK_SIZE
				if len(chunk) != expectedSize {
					t.Errorf("Last chunk expected to be %d bytes, got %d", expectedSize, len(chunk))
				}
			}
		}
	})

	t.Run("Multiple full chunks", func(t *testing.T) {
		// Create a test data exactly 2.5 times STREAM_CHUNK_SIZE
		fullChunks := 2
		extraBytes := STREAM_CHUNK_SIZE / 2

		testData := bytes.Repeat([]byte{42}, STREAM_CHUNK_SIZE*fullChunks+extraBytes)
		chunks := chunkBytes(testData)

		if len(chunks) != fullChunks+1 {
			t.Errorf("Expected %d chunks, got %d", fullChunks+1, len(chunks))
		}

		// Verify each full chunk is exactly STREAM_CHUNK_SIZE
		for i := 0; i < fullChunks; i++ {
			if len(chunks[i]) != STREAM_CHUNK_SIZE {
				t.Errorf("Chunk %d expected size %d, got %d", i, STREAM_CHUNK_SIZE, len(chunks[i]))
			}
		}

		// Verify the last partial chunk size
		if len(chunks[fullChunks]) != extraBytes {
			t.Errorf("Last chunk expected size %d, got %d", extraBytes, len(chunks[fullChunks]))
		}
	})
}

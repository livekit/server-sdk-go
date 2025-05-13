#!/bin/bash

if [ $# -ne 1 ]; then
    echo "Usage: $0 <pcm_file>"
    echo "Example: $0 audio.pcm"
    exit 1
fi

PCM_FILE=$1

# Check if file exists
if [ ! -f "$PCM_FILE" ]; then
    echo "Error: File '$PCM_FILE' not found"
    exit 1
fi

SAMPLE_RATE=48000
CHANNELS=2
BIT_DEPTH=16

FILE_SIZE=$(stat -f%z "$PCM_FILE")
BYTES_PER_SAMPLE=$((BIT_DEPTH / 8))
TOTAL_SAMPLES=$((FILE_SIZE / BYTES_PER_SAMPLE / CHANNELS))

DURATION=$(echo "$TOTAL_SAMPLES / $SAMPLE_RATE" | bc)

echo "PCM File: $PCM_FILE"
echo "File size: $FILE_SIZE bytes"
echo "Sample rate: $SAMPLE_RATE Hz"
echo "Channels: $CHANNELS (stereo)"
echo "Bit depth: $BIT_DEPTH bits"
echo "Duration: $DURATION seconds"
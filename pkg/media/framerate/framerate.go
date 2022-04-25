package framerate

import "math"

// Common video frame rates
var nums = []float64{24, 24000, 25, 30000, 30, 50, 60000, 60, 120}
var dens = []float64{1, 1001, 1, 1001, 1, 1, 1001, 1, 1}

func GetBestMatch(framerate float64) (uint32, uint32) {
	best := 0
	diff := math.Abs(nums[0]/dens[0] - framerate)

	for i := 1; i < len(nums); i++ {
		d := math.Abs(nums[i]/dens[i] - framerate)
		if d < diff {
			best = i
			diff = d
		}

		if diff == 0 {
			break
		}
	}

	return uint32(nums[best]), uint32(dens[best])
}

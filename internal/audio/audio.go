package audio

import (
	"fmt"
	"os/exec"
	"time"
)

const (
	recordSeconds = 5
)

// Recorder captures audio from a connected microphone.
type Recorder struct{}

func New() *Recorder {
	return &Recorder{}
}

// Record captures audio for recordSeconds and returns raw WAV bytes.
// Uses arecord (ALSA) — standard on Raspberry Pi OS.
func (r *Recorder) Record() ([]byte, error) {
	path := fmt.Sprintf("/tmp/gatemanai-audio-%d.wav", time.Now().UnixMilli())

	// -d duration in seconds, -f cd = 44100Hz 16bit stereo, -q = quiet
	cmd := exec.Command("arecord", "-d", fmt.Sprintf("%d", recordSeconds), "-f", "cd", "-q", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("arecord: %w — %s", err, out)
	}

	data, err := exec.Command("cat", path).Output()
	if err != nil {
		return nil, fmt.Errorf("read audio: %w", err)
	}

	return data, nil
}

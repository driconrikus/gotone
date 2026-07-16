package tui

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/rvaldez/gotone/engine"
)

const (
	meterWidth = 40
	minDB      = -60.0
	maxDB      = 0.0
)

// Row positions (1-indexed) for the mutable lines
const (
	rowMute   = 10
	rowGain   = 12
	rowChan   = 14
	rowInput  = 16
	rowOutput = 17
)

type TUI struct {
	eng       *engine.Engine
	stopCh    chan struct{}
	doneCh    chan struct{}

	inPeak, outPeak float64
	peakDecay       float64
	lastPeakTime    time.Time

	mu sync.Mutex
}

func New(eng *engine.Engine) *TUI {
	return &TUI{
		eng:          eng,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
		peakDecay:    0.9995,
		lastPeakTime: time.Now(),
	}
}

func (t *TUI) Done() <-chan struct{} {
	return t.doneCh
}

func (t *TUI) Start() {
	fmt.Print("\033[?25l") // hide cursor
	t.renderFull()
	go t.refreshLoop()
	go t.startInputLoop()
}

func (t *TUI) refreshLoop() {
	ticker := time.NewTicker(100 * time.Millisecond) // 10 fps
	defer ticker.Stop()
	for {
		select {
		case <-t.stopCh:
			return
		case <-ticker.C:
			t.renderUpdate()
		}
	}
}

func (t *TUI) Stop() {
	select {
	case <-t.stopCh:
	default:
		close(t.stopCh)
	}
	fmt.Print("\033[?25h\n") // show cursor
	select {
	case <-t.doneCh:
	default:
		close(t.doneCh)
	}
}

func (t *TUI) startInputLoop() {
	buf := make([]byte, 3)
	for {
		select {
		case <-t.stopCh:
			return
		default:
		}
		n, err := readRawInput(buf)
		if err != nil || n == 0 {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if t.handleInput(buf[:n]) {
			t.renderFull()
		}
	}
}

func (t *TUI) handleInput(b []byte) bool {
	if len(b) == 1 {
		switch b[0] {
		case 'q', 'Q', 3:
			t.Stop()
			return false
		case 'm', 'M':
			t.eng.ToggleMute()
			return true
		case ',':
			newBuf := t.eng.FramesPerBuffer() * 2
			if newBuf > 4096 {
				newBuf = 4096
			}
			if err := t.eng.ChangeBuffer(newBuf); err == nil {
				return true
			}
			return false
		case '.':
			newBuf := t.eng.FramesPerBuffer() / 2
			if newBuf < 16 {
				newBuf = 16
			}
			if err := t.eng.ChangeBuffer(newBuf); err == nil {
				return true
			}
			return false
		}
	}
	if len(b) == 3 && b[0] == 27 && b[1] == '[' {
		switch b[2] {
		case 'A':
			gain := t.eng.GetGain() + 1.0
			if gain > 24.0 {
				gain = 24.0
			}
			t.eng.SetGain(gain)
			return true
		case 'B':
			gain := t.eng.GetGain() - 1.0
			if gain < -60.0 {
				gain = -60.0
			}
			t.eng.SetGain(gain)
			return true
		case 'C':
			ch := t.eng.OutputChannel() + 1
			if ch > t.eng.OutputChannels() {
				ch = t.eng.OutputChannels()
			}
			t.eng.SetOutputChannel(ch)
			return true
		case 'D':
			ch := t.eng.OutputChannel() - 1
			if ch < 1 {
				ch = 1
			}
			t.eng.SetOutputChannel(ch)
			return true
		}
	}
	return false
}

// renderFull draws the entire UI (used on start and after layout changes)
func (t *TUI) renderFull() {
	t.mu.Lock()
	defer t.mu.Unlock()

	stats := t.eng.Stats()
	gainDB := t.eng.GetGain()
	muted := t.eng.IsMuted()
	inDB := t.computePeaks(stats)

	var sb strings.Builder
	sb.WriteString("\033[H\033[2J") // clear once

	sb.WriteString("\033[1;36m")
	sb.WriteString("  ╔══════════════════════════════════════════════════╗\n")
	sb.WriteString("  ║              GOTONE  —  Audio Monitor         ║\n")
	sb.WriteString("  ╚══════════════════════════════════════════════════╝\n")
	sb.WriteString("\033[0m\n")
	sb.WriteString(fmt.Sprintf("  \033[1mInput:  \033[0m%s\n", truncate(t.eng.InputDeviceName(), 42)))
	sb.WriteString(fmt.Sprintf("  \033[1mOutput: \033[0m%s\n", truncate(t.eng.OutputDeviceName(), 42)))
	bufMS := float64(t.eng.FramesPerBuffer()) / float64(t.eng.SampleRate()) * 1000
	sb.WriteString(fmt.Sprintf("  \033[1mRate:   \033[0m%d Hz    \033[1mBuffer:\033[0m %d frames (%.1f ms)\n",
		t.eng.SampleRate(), t.eng.FramesPerBuffer(), bufMS))
	sb.WriteString(fmt.Sprintf("  \033[1mLatency:\033[0m in %.1f ms + out %.1f ms = \033[1m%.1f ms total\033[0m\n\n",
		t.eng.InputLatencyMS(), t.eng.OutputLatencyMS(), t.eng.LatencyMS()))

	// Mute line (row 10)
	if muted {
		sb.WriteString("  \033[1;31m■ MUTED\033[0m\n\n")
	} else {
		sb.WriteString("  \033[1;32m▶ LIVE\033[0m\n\n")
	}
	// Gain line (row 12)
	gainStr := fmt.Sprintf("%+.1f dB", gainDB)
	if gainDB <= -60 {
		gainStr = "-∞ dB"
	}
	sb.WriteString(fmt.Sprintf("  \033[1mGain:  \033[0m%s\n\n", gainStr))
	// Output channel (row 14)
	sb.WriteString(fmt.Sprintf("  \033[1mChan:  \033[0m%d / %d\n\n", t.eng.OutputChannel(), t.eng.OutputChannels()))
	// Meters (rows 16-17)
	sb.WriteString("  \033[1mInput:  \033[0m")
	sb.WriteString(renderMeter(inDB, t.inPeak, "\033[32m"))
	sb.WriteString(fmt.Sprintf(" %6.1f dB\n", inDB))
	sb.WriteString("  \033[1mOutput: \033[0m")
	sb.WriteString(renderMeter(t.computeOutDB(), t.outPeak, "\033[34m"))
	sb.WriteString(fmt.Sprintf(" %6.1f dB\n\n", t.computeOutDB()))

	sb.WriteString("\033[90m")
	sb.WriteString("  ┌──────────────────────────────────────────────────────────────────┐\n")
	sb.WriteString("  │  ↑/↓  Gain   ←/→  Channel   </>  Buffer   m  Mute   q  Quit   │\n")
	sb.WriteString("  └──────────────────────────────────────────────────────────────────┘\n")
	sb.WriteString("\033[0m")

	fmt.Print(sb.String())
}

// renderUpdate redraws only the changing lines in-place
func (t *TUI) renderUpdate() {
	t.mu.Lock()
	defer t.mu.Unlock()

	stats := t.eng.Stats()
	gainDB := t.eng.GetGain()
	muted := t.eng.IsMuted()
	inDB := t.computePeaks(stats)
	outDB := t.computeOutDB()

	var sb strings.Builder

	// Row 10: mute status
	sb.WriteString(fmt.Sprintf("\033[%d;1H", rowMute))
	if muted {
		sb.WriteString("  \033[1;31m■ MUTED\033[0m\033[K")
	} else {
		sb.WriteString("  \033[1;32m▶ LIVE\033[0m\033[K")
	}

	// Row 12: gain
	sb.WriteString(fmt.Sprintf("\033[%d;1H", rowGain))
	gainStr := fmt.Sprintf("%+.1f dB", gainDB)
	if gainDB <= -60 {
		gainStr = "-∞ dB"
	}
	sb.WriteString(fmt.Sprintf("  \033[1mGain:  \033[0m%s\033[K", gainStr))

	// Row 14: output channel
	sb.WriteString(fmt.Sprintf("\033[%d;1H", rowChan))
	sb.WriteString(fmt.Sprintf("  \033[1mChan:  \033[0m%d / %d\033[K", t.eng.OutputChannel(), t.eng.OutputChannels()))

	// Row 16: input meter
	sb.WriteString(fmt.Sprintf("\033[%d;1H", rowInput))
	sb.WriteString("  \033[1mInput:  \033[0m")
	sb.WriteString(renderMeter(inDB, t.inPeak, "\033[32m"))
	sb.WriteString(fmt.Sprintf(" %6.1f dB\033[K", inDB))

	// Row 17: output meter
	sb.WriteString(fmt.Sprintf("\033[%d;1H", rowOutput))
	sb.WriteString("  \033[1mOutput: \033[0m")
	sb.WriteString(renderMeter(outDB, t.outPeak, "\033[34m"))
	sb.WriteString(fmt.Sprintf(" %6.1f dB\033[K", outDB))

	// Move cursor out of the way
	sb.WriteString("\033[20;1H")

	fmt.Print(sb.String())
}

func (t *TUI) computePeaks(stats engine.Stats) float64 {
	now := time.Now()
	elapsed := now.Sub(t.lastPeakTime).Seconds()
	t.lastPeakTime = now

	inDB := engine.RMSdB(stats.InputLevel)

	if stats.InputLevel > t.inPeak {
		t.inPeak = stats.InputLevel
	} else {
		t.inPeak *= math.Pow(t.peakDecay, elapsed*60)
	}
	if stats.OutputLevel > t.outPeak {
		t.outPeak = stats.OutputLevel
	} else {
		t.outPeak *= math.Pow(t.peakDecay, elapsed*60)
	}

	return inDB
}

func (t *TUI) computeOutDB() float64 {
	return engine.RMSdB(t.outPeak)
}

func renderMeter(level, peakVal float64, color string) string {
	normalized := (level - minDB) / (maxDB - minDB)
	if normalized < 0 {
		normalized = 0
	}
	if normalized > 1 {
		normalized = 1
	}

	filled := int(normalized * float64(meterWidth))
	if filled > meterWidth {
		filled = meterWidth
	}

	peakDB := engine.RMSdB(peakVal)
	peakPos := int(((peakDB - minDB) / (maxDB - minDB)) * float64(meterWidth))
	if peakPos > meterWidth {
		peakPos = meterWidth
	}

	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < meterWidth; i++ {
		if i == peakPos && peakPos > 0 {
			sb.WriteString("\033[97m│\033[0m")
		} else if i < filled {
			ratio := float64(i) / float64(meterWidth)
			if ratio < 0.6 {
				sb.WriteString(color)
			} else if ratio < 0.8 {
				sb.WriteString("\033[33m")
			} else {
				sb.WriteString("\033[31m")
			}
			sb.WriteString("█\033[0m")
		} else {
			sb.WriteString("\033[90m░\033[0m")
		}
	}
	sb.WriteString("]")
	return sb.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

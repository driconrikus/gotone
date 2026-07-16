package tui

import (
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rvaldez/gotone/engine"
	"golang.org/x/term"
)

const (
	minDB = -60.0
	maxDB = 0.0
)

var version = "dev"

type TUI struct {
	eng       *engine.Engine
	stopCh    chan struct{}
	doneCh    chan struct{}
	resizeCh  chan struct{}

	inPeak, outPeak float64
	peakDecay       float64
	lastPeakTime    time.Time

	termWidth  int
	termHeight int
	showHelp   bool

	mu sync.Mutex
}

func New(eng *engine.Engine) *TUI {
	w, h, _ := term.GetSize(int(1))
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	return &TUI{
		eng:          eng,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
		resizeCh:     make(chan struct{}, 1),
		peakDecay:    0.9995,
		lastPeakTime: time.Now(),
		termWidth:    w,
		termHeight:   h,
	}
}

func SetVersion(v string) {
	version = v
}

func (t *TUI) Done() <-chan struct{} {
	return t.doneCh
}

func (t *TUI) Start() {
	os.Stdout.Write([]byte("\033[?25l\033[2J\033[H")) // hide cursor, clear screen, home
	t.renderFull()
	go t.refreshLoop()
	go t.startInputLoop()
	go t.resizeListener()
}

func (t *TUI) resizeListener() {
	for {
		select {
		case <-t.stopCh:
			return
		case <-t.resizeCh:
			w, h, err := term.GetSize(int(1))
			if err != nil || w <= 0 || h <= 0 {
				continue
			}
			t.mu.Lock()
			t.termWidth = w
			t.termHeight = h
			t.mu.Unlock()
			t.renderFull()
		}
	}
}

func (t *TUI) NotifyResize() {
	select {
	case t.resizeCh <- struct{}{}:
	default:
	}
}

func (t *TUI) refreshLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-t.stopCh:
			return
		case <-ticker.C:
			t.renderFull()
		}
	}
}

func (t *TUI) Stop() {
	select {
	case <-t.stopCh:
	default:
		close(t.stopCh)
	}
	os.Stdout.Write([]byte("\033[?25h")) // show cursor
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
		case 'h', 'H':
			t.showHelp = !t.showHelp
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

func (t *TUI) meterWidth() int {
	mw := t.termWidth - 22
	if mw < 10 {
		mw = 10
	}
	if mw > 80 {
		mw = 80
	}
	return mw
}

func (t *TUI) boxWidth() int {
	bw := t.termWidth - 4
	if bw < 20 {
		bw = 20
	}
	return bw
}

func (t *TUI) renderFull() {
	t.mu.Lock()
	defer t.mu.Unlock()

	stats := t.eng.Stats()
	gainDB := t.eng.GetGain()
	muted := t.eng.IsMuted()
	inDB := t.computePeaks(stats)
	outDB := t.computeOutDB()

	bw := t.boxWidth()
	mw := t.meterWidth()

	var sb strings.Builder

	// Clear screen and home cursor
	sb.WriteString("\033[H\033[2J")

	if t.showHelp {
		t.renderHelp(&sb)
		os.Stdout.Write([]byte(sb.String()))
		return
	}

	line := func(row int, s string) {
		sb.WriteString(fmt.Sprintf("\033[%d;1H%s\033[K", row, s))
	}

	row := 1

	// Header box
	sb.WriteString("\033[1;36m")
	title := "GOTONE  —  Audio Monitor"
	titlePadding := displayLen(title)
	if titlePadding > bw-2 {
		titlePadding = bw - 2
	} else {
		titlePadding = bw - 2 - titlePadding
	}
	leftPad := titlePadding / 2
	rightPad := titlePadding - leftPad
	line(row, "  ╔"+strings.Repeat("═", bw-2)+"╗"); row++
	line(row, "  ║"+strings.Repeat(" ", leftPad)+title+strings.Repeat(" ", rightPad)+"║"); row++
	line(row, "  ╚"+strings.Repeat("═", bw-2)+"╝"); row++
	sb.WriteString("\033[0m")
	line(row, ""); row++

	// Device info
	line(row, fmt.Sprintf("  \033[1mInput:  \033[0m%s", truncate(t.eng.InputDeviceName(), bw-12))); row++
	line(row, fmt.Sprintf("  \033[1mOutput: \033[0m%s", truncate(t.eng.OutputDeviceName(), bw-12))); row++
	bufMS := float64(t.eng.FramesPerBuffer()) / float64(t.eng.SampleRate()) * 1000
	line(row, fmt.Sprintf("  \033[1mRate:   \033[0m%d Hz    \033[1mBuffer:\033[0m %d frames (%.1f ms)",
		t.eng.SampleRate(), t.eng.FramesPerBuffer(), bufMS)); row++
	line(row, fmt.Sprintf("  \033[1mLatency:\033[0m in %.1f ms + out %.1f ms = \033[1m%.1f ms total\033[0m",
		t.eng.InputLatencyMS(), t.eng.OutputLatencyMS(), t.eng.LatencyMS())); row++
	line(row, ""); row++

	// Mute
	if muted {
		line(row, "  \033[1;31m■ MUTED\033[0m"); row++
	} else {
		line(row, "  \033[1;32m▶ LIVE\033[0m"); row++
	}
	line(row, ""); row++

	// Gain
	gainStr := fmt.Sprintf("%+.1f dB", gainDB)
	if gainDB <= -60 {
		gainStr = "-∞ dB"
	}
	line(row, fmt.Sprintf("  \033[1mGain:  \033[0m%s", gainStr)); row++
	line(row, ""); row++

	// Channel
	line(row, fmt.Sprintf("  \033[1mOutput Channel:  \033[0m%d / %d", t.eng.OutputChannel(), t.eng.OutputChannels())); row++
	line(row, ""); row++

	// Meters
	line(row, "  \033[1mInput:  \033[0m"+renderMeter(inDB, t.inPeak, "\033[32m", mw)+fmt.Sprintf(" %6.1f dB", inDB)); row++
	line(row, "  \033[1mOutput: \033[0m"+renderMeter(outDB, t.outPeak, "\033[34m", mw)+fmt.Sprintf(" %6.1f dB", outDB)); row++
	line(row, ""); row++

	// Help bar
	helpText := "  ↑/↓  Gain   ←/→  Channel   </>  Buffer   m  Mute   h  Help   q  Quit"
	helpTextLen := displayLen(helpText)
	helpBoxWidth := bw
	if helpBoxWidth < helpTextLen+4 {
		helpBoxWidth = helpTextLen + 4
	}
	helpInner := helpBoxWidth - 2
	helpContent := helpText
	if helpTextLen > helpInner {
		helpContent = truncate(helpText, helpInner)
	}
	helpContentLen := displayLen(helpContent)
	if helpContentLen < helpInner {
		helpContent += strings.Repeat(" ", helpInner-helpContentLen)
	}
	sb.WriteString("\033[90m")
	line(row, "  ┌"+strings.Repeat("─", helpInner)+"┐"); row++
	line(row, "  │"+helpContent+"│"); row++
	line(row, "  └"+strings.Repeat("─", helpInner)+"┘"); row++
	line(row, fmt.Sprintf("  gotone %s", version)); row++
	sb.WriteString("\033[0m")

	// Blank remaining rows
	for ; row <= t.termHeight; row++ {
		sb.WriteString(fmt.Sprintf("\033[%d;1H\033[K", row))
	}

	os.Stdout.Write([]byte(sb.String()))
}

func (t *TUI) renderHelp(sb *strings.Builder) {
	bw := t.boxWidth()
	lines := []string{
		"",
		"  \033[1m↑\033[0m/\033[1m↓\033[0m    Gain          Adjust level (+/- 1 dB)",
		"  \033[1m←\033[0m/\033[1m→\033[0m    Channel       Change output channel",
		"  \033[1m,\033[0m/\033[1m.\033[0m    Buffer        Increase/decrease buffer size",
		"  \033[1mm\033[0m      Mute          Toggle mute on/off",
		"  \033[1mh\033[0m      Help          Show/hide this help",
		"  \033[1mq\033[0m      Quit          Exit the application",
		"",
		"  Press \033[1mh\033[0m to go back or \033[1mq\033[0m to close",
	}
	helpHeight := len(lines) + 2
	startRow := (t.termHeight - helpHeight) / 2
	if startRow < 1 {
		startRow = 1
	}

	sb.WriteString("\033[1;33m")
	top := "  ╔" + strings.Repeat("═", bw-2) + "╗"
	sb.WriteString(fmt.Sprintf("\033[%d;1H%s\033[K", startRow, top))
	for i, l := range lines {
		padded := padBoxLine(l, bw-4)
		sb.WriteString(fmt.Sprintf("\033[%d;1H  ║ %s ║\033[K", startRow+1+i, padded))
	}
	bot := "  ╚" + strings.Repeat("═", bw-2) + "╝"
	sb.WriteString(fmt.Sprintf("\033[%d;1H%s\033[K", startRow+1+len(lines), bot))
	sb.WriteString("\033[0m")
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

func renderMeter(level, peakVal float64, color string, meterWidth int) string {
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
			sb.WriteString("\033[97m|\033[0m")
		} else if i < filled {
			ratio := float64(i) / float64(meterWidth)
			if ratio < 0.6 {
				sb.WriteString(color)
			} else if ratio < 0.8 {
				sb.WriteString("\033[33m")
			} else {
				sb.WriteString("\033[31m")
			}
			sb.WriteString("#\033[0m")
		} else {
			sb.WriteString("\033[90m.\033[0m")
		}
	}
	sb.WriteString("]")
	return sb.String()
}

func truncate(s string, max int) string {
	vl := displayLen(s)
	if vl <= max {
		return s
	}
	if max < 3 {
		runes := []rune(s)
		return string(runes[:max])
	}
	runes := []rune(s)
	return string(runes[:max-3]) + "..."
}

func stripANSI(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\033' && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for ; i < len(s); i++ {
				if ch := s[i]; ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' {
					break
				}
			}
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

func displayLen(s string) int {
	return len([]rune(s))
}

func padBoxLine(s string, width int) string {
	vis := stripANSI(s)
	vlen := displayLen(vis)
	if vlen > width {
		limit := width + (len(s) - len(vis))
		if limit > len(s) {
			limit = len(s)
		}
		s = s[:limit]
		vis = stripANSI(s)
		vlen = displayLen(vis)
	}
	return s + strings.Repeat(" ", width-vlen)
}

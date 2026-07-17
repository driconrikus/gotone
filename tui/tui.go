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
	showEQ     bool
	showPresets bool
	eqBand     int
	presetIdx   int

	saveFlashMsg   string
	saveFlashCount int

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
	os.Stdout.Write([]byte("\033[?1049h\033[?25l")) // enter alternate screen, hide cursor
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
	os.Stdout.Write([]byte("\033[?25h\033[?1049l")) // show cursor, leave alternate screen
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
		case 'e', 'E':
			t.showEQ = !t.showEQ
			return true
		case 'p', 'P':
			t.showPresets = !t.showPresets
			if t.showPresets {
				t.showEQ = true
			}
			return true
		case '\r', '\n':
			if t.showPresets {
				pn := t.eng.PresetCount()
				if t.presetIdx >= 0 && t.presetIdx < pn {
					t.eng.ApplyPreset(t.presetIdx)
				}
				t.showPresets = false
				return true
			}
		case 'S':
			if t.showPresets {
				bpn := len(engine.BuiltinPresets)
				if t.presetIdx >= bpn && t.presetIdx < t.eng.PresetCount() {
					t.eng.SaveCustom(t.presetIdx - bpn)
					t.showPresets = false
					t.saveFlashMsg = fmt.Sprintf("Saved to %s", t.eng.CustomSlotName(t.presetIdx-bpn))
					t.saveFlashCount = 20
				} else {
					t.saveFlashMsg = "Select Custom 1/2/3 to save"
					t.saveFlashCount = 20
				}
				return true
			}
		case 's':
			if t.showPresets {
				bpn := len(engine.BuiltinPresets)
				if t.presetIdx >= bpn && t.presetIdx < t.eng.PresetCount() {
					slot := t.presetIdx - bpn
					t.eng.SaveCustom(slot)
					t.showPresets = false
					t.saveFlashMsg = fmt.Sprintf("Saved to %s", t.eng.CustomSlotName(slot))
					t.saveFlashCount = 20
				} else {
					t.saveFlashMsg = "Select Custom 1/2/3 to save"
					t.saveFlashCount = 20
				}
			} else {
				name, ok := t.eng.QuickSave()
				if ok {
					t.saveFlashMsg = fmt.Sprintf("Saved to %s", name)
				} else {
					t.saveFlashMsg = "Select a custom preset first"
				}
				t.saveFlashCount = 20
			}
			return true
		case 'r', 'R':
			if t.showEQ {
				t.eng.ResetEQ()
				return true
			}
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
			if t.showPresets {
				t.presetIdx--
				if t.presetIdx < 0 {
					t.presetIdx = t.eng.PresetCount() - 1
				}
				t.eng.ApplyPreset(t.presetIdx)
			} else if t.showEQ {
				t.eng.SetEQBand(t.eqBand, t.eng.EQBandGain(t.eqBand)+1)
			} else {
				gain := t.eng.GetGain() + 1.0
				if gain > 24.0 {
					gain = 24.0
				}
				t.eng.SetGain(gain)
			}
			return true
		case 'B':
			if t.showPresets {
				t.presetIdx++
				if t.presetIdx >= t.eng.PresetCount() {
					t.presetIdx = 0
				}
				t.eng.ApplyPreset(t.presetIdx)
			} else if t.showEQ {
				t.eng.SetEQBand(t.eqBand, t.eng.EQBandGain(t.eqBand)-1)
			} else {
				gain := t.eng.GetGain() - 1.0
				if gain < -60.0 {
					gain = -60.0
				}
				t.eng.SetGain(gain)
			}
			return true
		case 'C':
			if t.showPresets {
				t.presetIdx++
				if t.presetIdx >= t.eng.PresetCount() {
					t.presetIdx = 0
				}
				t.eng.ApplyPreset(t.presetIdx)
			} else if t.showEQ {
				t.eqBand++
				if t.eqBand >= t.eng.EQBands() {
					t.eqBand = t.eng.EQBands() - 1
				}
			} else {
				ch := t.eng.OutputChannel() + 1
				if ch > t.eng.OutputChannels() {
					ch = t.eng.OutputChannels()
				}
				t.eng.SetOutputChannel(ch)
			}
			return true
		case 'D':
			if t.showPresets {
				t.presetIdx--
				if t.presetIdx < 0 {
					t.presetIdx = t.eng.PresetCount() - 1
				}
				t.eng.ApplyPreset(t.presetIdx)
			} else if t.showEQ {
				t.eqBand--
				if t.eqBand < 0 {
					t.eqBand = 0
				}
			} else {
				ch := t.eng.OutputChannel() - 1
				if ch < 1 {
					ch = 1
				}
				t.eng.SetOutputChannel(ch)
			}
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

	// Home cursor
	sb.WriteString("\033[H")

	if t.showEQ {
		t.renderEQ(&sb, bw)
	} else if t.showHelp {
		t.renderHelp(&sb)
	} else {
		line := func(row int, s string) {
			if row < 1 || row > t.termHeight {
				return
			}
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
		line(row, fmt.Sprintf("  \033[1mEQ Preset:\033[0m %s", t.eng.CurrentPresetName())); row++
		line(row, ""); row++

		// Channel
		line(row, fmt.Sprintf("  \033[1mOutput Channel:  \033[0m%d / %d", t.eng.OutputChannel(), t.eng.OutputChannels())); row++
		line(row, ""); row++

		// Meters
		line(row, "  \033[1mInput:  \033[0m"+renderMeter(inDB, t.inPeak, "\033[32m", mw)+fmt.Sprintf(" %6.1f dB", inDB)); row++
		line(row, "  \033[1mOutput: \033[0m"+renderMeter(outDB, t.outPeak, "\033[34m", mw)+fmt.Sprintf(" %6.1f dB", outDB)); row++
		line(row, ""); row++

		// Blank rows between content and bottom bar
		helpBarTop := t.termHeight - 4
		for ; row < helpBarTop; row++ {
			sb.WriteString(fmt.Sprintf("\033[%d;1H\033[K", row))
		}
		row = helpBarTop

		// Help bar (anchored to bottom)
		helpText := "  ↑/↓  Gain   ←/→  Channel   </>  Buffer   m  Mute   e  Equalizer   p  Presets   h  Help   q  Quit"
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

		for ; row <= t.termHeight; row++ {
			sb.WriteString(fmt.Sprintf("\033[%d;1H\033[K", row))
		}
	}

	if t.showPresets {
		t.renderPresets(&sb, bw)
	}

	if t.saveFlashCount > 0 {
		flash := t.saveFlashMsg
		flashLen := displayLen(flash)
		flashBox := flashLen + 4
		if flashBox > bw {
			flashBox = bw
		}
		flashRow := t.termHeight - 2
		flashCol := (bw - flashBox) / 2
		pad := flashBox - flashLen - 2
		left := pad / 2
		right := pad - left
		sb.WriteString(fmt.Sprintf("\033[%d;%dH\033[1;33m╔%s╗\033[0m", flashRow, flashCol, strings.Repeat("═", flashBox-2)))
		sb.WriteString(fmt.Sprintf("\033[%d;%dH\033[1;33m║\033[1;97m%s\033[1;33m║\033[0m", flashRow+1, flashCol, strings.Repeat(" ", left)+flash+strings.Repeat(" ", right)))
		sb.WriteString(fmt.Sprintf("\033[%d;%dH\033[1;33m╚%s╝\033[0m", flashRow+2, flashCol, strings.Repeat("═", flashBox-2)))
		t.saveFlashCount--
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
		"  \033[1me\033[0m      Equalizer     Show/hide 10-band EQ",
		"  \033[1mp\033[0m      Presets       Browse and apply EQ presets",
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

func (t *TUI) renderPresets(sb *strings.Builder, bw int) {
	names := t.eng.PresetNames()

	var lines []string
	for i, name := range names {
		var line string
		if i == t.presetIdx {
			line = fmt.Sprintf(" \033[1;97m▸\033[0m %s", name)
		} else {
			line = fmt.Sprintf("   %s", name)
		}
		if i >= len(engine.BuiltinPresets) {
			line += "  \033[90m[S to save]\033[0m"
		}
		lines = append(lines, line)
	}

	innerWidth := 0
	for _, l := range lines {
		w := displayLen(stripANSI(l))
		if w > innerWidth {
			innerWidth = w
		}
	}
	if innerWidth < 20 {
		innerWidth = 20
	}
	boxWidth := innerWidth + 4
	if boxWidth > bw {
		boxWidth = bw
	}
	innerWidth = boxWidth - 4

	boxHeight := len(lines) + 6
	startRow := (t.termHeight - boxHeight) / 2
	if startRow < 1 {
		startRow = 1
	}

	sb.WriteString("\033[1;36m")
	title := "EQ Presets"
	titlePad := (boxWidth - 2 - displayLen(title)) / 2
	top := "  ╔" + strings.Repeat("═", boxWidth-2) + "╗"
	sb.WriteString(fmt.Sprintf("\033[%d;1H%s\033[K", startRow, top))
	row := startRow + 1
	sb.WriteString(fmt.Sprintf("\033[%d;1H  ║"+strings.Repeat(" ", titlePad)+title+strings.Repeat(" ", boxWidth-2-titlePad-displayLen(title))+"║\033[K", row))
	row++
	sb.WriteString("\033[0m")
	sb.WriteString(fmt.Sprintf("\033[%d;1H  ╠"+strings.Repeat("═", boxWidth-2)+"╣\033[K", row))
	row++

	for _, l := range lines {
		padded := padBoxLine(l, innerWidth)
		sb.WriteString(fmt.Sprintf("\033[%d;1H  ║ %s ║", row, padded))
		row++
	}

	sb.WriteString("\033[90m")
	bot := "  ╚" + strings.Repeat("═", boxWidth-2) + "╝"
	sb.WriteString(fmt.Sprintf("\033[%d;1H%s\033[K", row, bot))
	row++
	helpLine := "  \033[90m↑/↓\033[0m Select  \033[90mEnter\033[0m Apply  \033[90mS\033[0m Save  \033[90mp\033[0m Close"
	sb.WriteString(fmt.Sprintf("\033[%d;1H%s", row, helpLine))
	sb.WriteString("\033[0m")
}

func (t *TUI) renderEQ(sb *strings.Builder, bw int) {
	// Clear every row to prevent scrollback artifacts
	for i := 1; i <= t.termHeight; i++ {
		sb.WriteString(fmt.Sprintf("\033[%d;1H\033[K", i))
	}

	freqs := t.eng.EQFrequencies()
	bands := t.eng.EQBands()

	line := func(row int, s string) {
		if row < 1 || row > t.termHeight {
			return
		}
		sb.WriteString(fmt.Sprintf("\033[%d;1H%s\033[K", row, s))
	}

	dbRows := 25
	centerRow := 12

	stepChars := []string{"██", "▇▇", "▆▆", "▅▅", "▃▃", "▂▂", "▁▁", "▓▓", "▒▒", "░░", "··", "  "}

	contentHeight := dbRows + 4
	helpBarTop := t.termHeight - 4
	titleEnd := 6
	spaceForContent := helpBarTop - titleEnd
	contentStartRow := titleEnd
	if contentHeight < spaceForContent {
		contentStartRow = titleEnd + (spaceForContent-contentHeight)/2
	}

	row := 1

	// Title box (always anchored to top)
	sb.WriteString("\033[1;36m")
	title := "Equalizer"
	titlePad := (bw - 2 - displayLen(title)) / 2
	line(row, "  ╔"+strings.Repeat("═", bw-2)+"╗"); row++
	line(row, "  ║"+strings.Repeat(" ", titlePad)+title+strings.Repeat(" ", bw-2-titlePad-displayLen(title))+"║"); row++
	line(row, "  ╚"+strings.Repeat("═", bw-2)+"╝"); row++
	sb.WriteString("\033[0m")
	line(row, fmt.Sprintf("  \033[1mPreset:\033[0m %s", t.eng.CurrentPresetName())); row++
	line(row, ""); row++

	// Build frequency labels with Hz/kHz
	colWidth := 5
	freqLabels := make([]string, bands)
	for b := 0; b < bands; b++ {
		freq := freqs[b]
		var label string
		if freq >= 1000 {
			khz := freq / 1000
			if khz == math.Trunc(khz) {
				label = fmt.Sprintf("%.0fkHz", khz)
			} else {
				label = fmt.Sprintf("%.1fkHz", khz)
			}
		} else {
			label = fmt.Sprintf("%.0fHz", freq)
		}
		if len(label) > colWidth {
			colWidth = len(label)
		}
		freqLabels[b] = label
	}

	scaleWidth := 4 // "+12┤" = 4 visible chars
	availableForBars := bw - scaleWidth
	if maxCol := availableForBars / bands; colWidth > maxCol {
		colWidth = maxCol
	}
	if colWidth < 2 {
		colWidth = 2
	}
	totalBarWidth := bands * colWidth
	totalWidth := scaleWidth + totalBarWidth
	groupStartCol := (bw - totalWidth) / 2
	if groupStartCol < 1 {
		groupStartCol = 1
	}

	dbLabels := []string{"+12", " +6", "  0", " -6", "-12"}

	// Helper: center a string in a field of given width
	centerStr := func(s string, w int) string {
		sl := displayLen(s)
		if sl >= w {
			return s
		}
		left := (w - sl) / 2
		right := w - sl - left
		return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
	}

	row = contentStartRow

	// --- Draw bar rows ---
	for i := 0; i < dbRows; i++ {
		var sb2 strings.Builder

		// Scale: dB label + ┤
		sb2.WriteString(strings.Repeat(" ", groupStartCol))
		if i == 0 || i == 6 || i == 12 || i == 18 || i == 24 {
			idx := i / 6
			sb2.WriteString(fmt.Sprintf("\033[90m%s┤\033[0m", dbLabels[idx]))
		} else {
			sb2.WriteString("\033[90m    \033[0m") // 4 spaces + reset = scaleWidth - 1 = 4
		}

		// Bars
		for b := 0; b < bands; b++ {
			gain := t.eng.EQBandGain(b)
			selected := b == t.eqBand

			gainRowsUp := 0
			gainRowsDown := 0
			if gain > 0 {
				gainRowsUp = gain
				if gainRowsUp > 12 {
					gainRowsUp = 12
				}
			} else if gain < 0 {
				gainRowsDown = -gain
				if gainRowsDown > 12 {
					gainRowsDown = 12
				}
			}

			isInPositiveFill := gainRowsUp > 0 && i < centerRow && i >= centerRow-gainRowsUp
			isInNegativeFill := gainRowsDown > 0 && i > centerRow && i <= centerRow+gainRowsDown
			isTinyPositive := gain > 0 && gainRowsUp == 0 && i == centerRow-1
			isTinyNegative := gain < 0 && gainRowsDown == 0 && i == centerRow+1

			var cell string
			switch {
			case i == centerRow:
				if selected {
					cell = "\033[1;97m━━\033[0m"
				} else {
					cell = "\033[90m━━\033[0m"
				}
			case isInPositiveFill:
				dist := centerRow - i
				isTip := i == centerRow-gainRowsUp
				if isTip {
					if selected {
						cell = "\033[1;97m▀▀\033[0m"
					} else {
						cell = "\033[32m▀▀\033[0m"
					}
				} else {
					body := stepChars[dist-1]
					if selected {
						cell = "\033[97m" + body + "\033[0m"
					} else {
						cell = "\033[32m" + body + "\033[0m"
					}
				}
			case isInNegativeFill:
				dist := i - centerRow
				isTip := i == centerRow+gainRowsDown
				if isTip {
					if selected {
						cell = "\033[1;97m▄▄\033[0m"
					} else {
						cell = "\033[31m▄▄\033[0m"
					}
				} else {
					body := stepChars[dist-1]
					if selected {
						cell = "\033[97m" + body + "\033[0m"
					} else {
						cell = "\033[31m" + body + "\033[0m"
					}
				}
			case isTinyPositive:
				if selected {
					cell = "\033[1;97m▀▀\033[0m"
				} else {
					cell = "\033[32m▀▀\033[0m"
				}
			case isTinyNegative:
				if selected {
					cell = "\033[1;97m▄▄\033[0m"
				} else {
					cell = "\033[31m▄▄\033[0m"
				}
			default:
				cell = "  "
			}

			sb2.WriteString(centerStr(cell, colWidth))
		}
		line(row, sb2.String())
		row++
	}

	// --- Frequency labels ---
	row++
	var freqLine strings.Builder
	freqLine.WriteString(strings.Repeat(" ", groupStartCol+scaleWidth))
	for b := 0; b < bands; b++ {
		freqLine.WriteString(centerStr(freqLabels[b], colWidth))
	}
	line(row, freqLine.String())
	row++

	// --- Gain values ---
	var gainLine strings.Builder
	gainLine.WriteString(strings.Repeat(" ", groupStartCol+scaleWidth))
	for b := 0; b < bands; b++ {
		g := t.eng.EQBandGain(b)
		var gstr string
		if g > 0 {
			gstr = fmt.Sprintf("+%d", g)
		} else if g < 0 {
			gstr = fmt.Sprintf("%d", g)
		} else {
			gstr = "0"
		}
		if b == t.eqBand {
			gainLine.WriteString(centerStr(fmt.Sprintf("\033[1;97m%s\033[0m", gstr), colWidth))
		} else {
			gainLine.WriteString(centerStr(gstr, colWidth))
		}
	}
	line(row, gainLine.String())
	row++

	// dB reference
	line(row, "  \033[90m+12 dB max · 0 dB flat · -12 dB min\033[0m"); row++

	// Blank rows between content and bottom bar
	for ; row < helpBarTop; row++ {
		sb.WriteString(fmt.Sprintf("\033[%d;1H\033[K", row))
	}
	row = helpBarTop

	// Help bar (anchored to bottom)
	helpText := "  \033[90m←/→\033[0m Band   \033[90m↑/↓\033[0m Gain   \033[90mr\033[0m Reset   \033[90mm\033[0m Mute   \033[90mp\033[0m Presets   \033[90me\033[0m Back   \033[90mq\033[0m Quit"
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

	for ; row <= t.termHeight; row++ {
		sb.WriteString(fmt.Sprintf("\033[%d;1H\033[K", row))
	}
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
	return len([]rune(stripANSI(s)))
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

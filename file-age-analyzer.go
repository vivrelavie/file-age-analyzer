package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── Domain types ─────────────────────────────────────────────────────────────

type AgeUnit int

const (
	UnitDays AgeUnit = iota
	UnitWeeks
	UnitMonths
	UnitYears
)

func (u AgeUnit) String() string { return [...]string{"days", "weeks", "months", "years"}[u] }

type SortField int

const (
	SortAge SortField = iota
	SortSize
	SortName
)

func (s SortField) String() string { return [...]string{"age", "size", "name"}[s] }

const defaultIgnoreFileName = ".file-age-analyzerignore"

type ViewState int

const (
	StateList ViewState = iota
	StateHelp
	StateConfirm
	StateIgnoreEditor
)

type InputFocus int

const (
	FocusPath InputFocus = iota
	FocusDepth
	FocusList
)

func (f InputFocus) next() InputFocus {
	switch f {
	case FocusPath:
		return FocusDepth
	case FocusDepth:
		return FocusList
	default:
		return FocusPath
	}
}

func (f InputFocus) prev() InputFocus {
	switch f {
	case FocusPath:
		return FocusList
	case FocusDepth:
		return FocusPath
	default:
		return FocusDepth
	}
}

type FileEntry struct {
	Path      string
	Size      int64
	ModTime   time.Time
	AgeString string
	Selected  bool
}

type ignoreMatcher struct {
	patterns []ignorePattern
}

type ignorePattern struct {
	regex *regexp.Regexp
}

// ─── Messages ─────────────────────────────────────────────────────────────────

type msgFilesFound struct {
	id    int
	files []FileEntry
}

type msgDeleteDone struct {
	count int
	size  int64
	errs  int
}

// ─── Model ────────────────────────────────────────────────────────────────────

type Model struct {
	// Config (live-adjustable in TUI)
	scanPath    string
	pathInput   string
	pathCursor  int
	ageValue    int
	ageUnit     AgeUnit
	dryRun      bool
	extensions  string
	maxDepth    int
	depthInput  string
	depthCursor int

	// State
	files             []FileEntry
	cursor            int
	scrollOffset      int
	sortBy            SortField
	sortDesc          bool
	viewState         ViewState
	inputFocus        InputFocus
	isScanning        bool
	scanSeq           int
	activeScanID      int
	lastScanSignature string
	ignoreDraft       string
	ignoreCursor      int
	ignoreStatusMsg   string

	// UI
	width  int
	height int

	// Feedback
	statusMsg  string
	lastAction string
}

func (m Model) Init() tea.Cmd {
	if !m.isScanning || m.scanPath == "" {
		return nil
	}
	return doScan(m.scanPath, m.ageValue, m.ageUnit, m.maxDepth, m.extensions, m.activeScanID)
}

// ─── Commands ─────────────────────────────────────────────────────────────────

func doScan(scanPath string, ageValue int, ageUnit AgeUnit, maxDepth int, extensions string, scanID int) tea.Cmd {
	return func() tea.Msg {
		threshold := computeThreshold(ageValue, ageUnit)
		return msgFilesFound{
			id:    scanID,
			files: findFiles(scanPath, threshold, maxDepth, extensions),
		}
	}
}

func doDelete(files []FileEntry, dryRun bool) tea.Cmd {
	return func() tea.Msg {
		count, size, errs := 0, int64(0), 0
		for _, f := range files {
			if !f.Selected {
				continue
			}
			if dryRun {
				count++
				size += f.Size
			} else {
				if err := os.Remove(f.Path); err != nil {
					errs++
				} else {
					count++
					size += f.Size
				}
			}
		}
		return msgDeleteDone{count, size, errs}
	}
}

// ─── Update ───────────────────────────────────────────────────────────────────

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case msgFilesFound:
		if msg.id != m.activeScanID {
			return m, nil
		}
		m.isScanning = false
		m.files = msg.files
		m.applySort()
		m.cursor, m.scrollOffset = 0, 0
		if len(m.files) == 0 {
			m.statusMsg = fmt.Sprintf("No files older than %d %s found in %s", m.ageValue, m.ageUnit, m.pathInput)
		} else {
			m.statusMsg = ""
		}

	case msgDeleteDone:
		if m.dryRun {
			m.lastAction = fmt.Sprintf("[DRY-RUN] Would delete %d file(s) — %s freed", msg.count, formatSize(msg.size))
		} else {
			m.lastAction = fmt.Sprintf("Deleted %d file(s), freed %s", msg.count, formatSize(msg.size))
			remaining := m.files[:0]
			for _, f := range m.files {
				if !f.Selected {
					remaining = append(remaining, f)
				}
			}
			m.files = remaining
			if m.cursor >= len(m.files) {
				m.cursor = max(0, len(m.files)-1)
			}
			m.clampScroll()
		}
		if msg.errs > 0 {
			m.lastAction += fmt.Sprintf(" (%d errors)", msg.errs)
		}
		m.viewState = StateList

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}

	switch m.viewState {
	case StateHelp:
		m.viewState = StateList
		return m, nil

	case StateConfirm:
		key := msg.String()
		if key == "y" || key == "Y" {
			m.viewState = StateList
			return m, doDelete(m.files, m.dryRun)
		}
		m.viewState = StateList
		return m, nil

	case StateIgnoreEditor:
		return m.handleIgnoreEditorKey(msg)

	case StateList:
		if m.inputFocus != FocusList {
			return m.handleInputKey(msg)
		}
		return m.handleListKey(msg)
	}

	return m, nil
}

func (m Model) handleInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyTab:
		m.inputFocus = m.inputFocus.next()
		return m, nil
	case tea.KeyShiftTab:
		m.inputFocus = m.inputFocus.prev()
		return m, nil
	case tea.KeyEnter, tea.KeyEsc:
		m.inputFocus = FocusList
		return m, nil
	}

	value, cursor := m.focusedInput()
	runes := []rune(value)
	cursor = min(max(0, cursor), len(runes))

	edited := false
	switch msg.Type {
	case tea.KeyLeft:
		cursor = max(0, cursor-1)
	case tea.KeyRight:
		cursor = min(len(runes), cursor+1)
	case tea.KeyHome:
		cursor = 0
	case tea.KeyEnd:
		cursor = len(runes)
	case tea.KeyBackspace, tea.KeyCtrlH:
		if cursor > 0 {
			runes = append(runes[:cursor-1], runes[cursor:]...)
			cursor--
			edited = true
		}
	case tea.KeyDelete:
		if cursor < len(runes) {
			runes = append(runes[:cursor], runes[cursor+1:]...)
			edited = true
		}
	case tea.KeySpace:
		if m.inputFocus == FocusPath {
			runes = append(runes[:cursor], append([]rune{' '}, runes[cursor:]...)...)
			cursor++
			edited = true
		}
	case tea.KeyRunes:
		insert := msg.Runes
		if m.inputFocus == FocusDepth {
			filtered := make([]rune, 0, len(insert))
			for _, r := range insert {
				if r >= '0' && r <= '9' {
					filtered = append(filtered, r)
				}
			}
			insert = filtered
		}
		if len(insert) > 0 {
			runes = append(runes[:cursor], append(insert, runes[cursor:]...)...)
			cursor += len(insert)
			edited = true
		}
	default:
		return m, nil
	}

	m.setFocusedInput(string(runes), cursor)
	if !edited {
		return m, nil
	}

	return m.startScan(false)
}

func (m Model) handleIgnoreEditorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlS:
		if m.scanPath == "" {
			m.ignoreStatusMsg = "Set a valid scan path before saving ignore rules"
			return m, nil
		}

		if err := saveIgnoreFileContents(m.scanPath, m.ignoreDraft); err != nil {
			m.ignoreStatusMsg = fmt.Sprintf("Could not save %s: %v", defaultIgnoreFileName, err)
			return m, nil
		}

		nextModel, cmd := m.startScan(true)
		next := nextModel.(Model)
		next.viewState = StateList
		next.ignoreStatusMsg = ""
		next.lastAction = "Saved " + defaultIgnoreFileName
		return next, cmd
	case tea.KeyEsc:
		m.viewState = StateList
		m.ignoreStatusMsg = ""
		return m, nil
	case tea.KeyEnter:
		m.ignoreDraft, m.ignoreCursor = insertTextAtCursor(m.ignoreDraft, m.ignoreCursor, "\n")
		m.ignoreStatusMsg = ""
		return m, nil
	case tea.KeyTab:
		m.ignoreDraft, m.ignoreCursor = insertTextAtCursor(m.ignoreDraft, m.ignoreCursor, "\t")
		m.ignoreStatusMsg = ""
		return m, nil
	case tea.KeyBackspace, tea.KeyCtrlH:
		m.ignoreDraft, m.ignoreCursor = deleteBeforeCursor(m.ignoreDraft, m.ignoreCursor)
		m.ignoreStatusMsg = ""
		return m, nil
	case tea.KeyDelete:
		m.ignoreDraft, m.ignoreCursor = deleteAtCursor(m.ignoreDraft, m.ignoreCursor)
		m.ignoreStatusMsg = ""
		return m, nil
	case tea.KeyLeft:
		m.ignoreCursor = max(0, m.ignoreCursor-1)
		return m, nil
	case tea.KeyRight:
		m.ignoreCursor = min(len([]rune(m.ignoreDraft)), m.ignoreCursor+1)
		return m, nil
	case tea.KeyUp:
		m.ignoreCursor = moveCursorVertical(m.ignoreDraft, m.ignoreCursor, -1)
		return m, nil
	case tea.KeyDown:
		m.ignoreCursor = moveCursorVertical(m.ignoreDraft, m.ignoreCursor, 1)
		return m, nil
	case tea.KeyHome:
		start, _ := currentLineBounds([]rune(m.ignoreDraft), m.ignoreCursor)
		m.ignoreCursor = start
		return m, nil
	case tea.KeyEnd:
		_, end := currentLineBounds([]rune(m.ignoreDraft), m.ignoreCursor)
		m.ignoreCursor = end
		return m, nil
	case tea.KeyRunes:
		if len(msg.Runes) == 0 {
			return m, nil
		}
		m.ignoreDraft, m.ignoreCursor = insertTextAtCursor(m.ignoreDraft, m.ignoreCursor, string(msg.Runes))
		m.ignoreStatusMsg = ""
		return m, nil
	default:
		return m, nil
	}
}

func (m Model) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyTab {
		m.inputFocus = FocusPath
		return m, nil
	}
	if msg.Type == tea.KeyShiftTab {
		m.inputFocus = FocusDepth
		return m, nil
	}

	key := msg.String()
	lh := m.listHeight()

	switch key {
	// ── Navigation ──────────────────────────────────────────────────────────
	case "j", "down":
		if m.cursor < len(m.files)-1 {
			m.cursor++
			if m.cursor >= m.scrollOffset+lh {
				m.scrollOffset++
			}
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
			if m.cursor < m.scrollOffset {
				m.scrollOffset--
			}
		}
	case "g", "home":
		m.cursor, m.scrollOffset = 0, 0
	case "G", "end":
		m.cursor = max(0, len(m.files)-1)
		m.scrollOffset = max(0, len(m.files)-lh)
	case "ctrl+d":
		m.cursor = min(m.cursor+lh/2, max(0, len(m.files)-1))
		m.clampScroll()
	case "ctrl+u":
		m.cursor = max(0, m.cursor-lh/2)
		m.clampScroll()

	// ── Selection ───────────────────────────────────────────────────────────
	case " ", "enter":
		if len(m.files) > 0 {
			m.files[m.cursor].Selected = !m.files[m.cursor].Selected
			if m.cursor < len(m.files)-1 {
				m.cursor++
				if m.cursor >= m.scrollOffset+lh {
					m.scrollOffset++
				}
			}
		}
	case "a":
		for i := range m.files {
			m.files[i].Selected = true
		}
	case "A":
		anySelected := false
		for _, f := range m.files {
			if f.Selected {
				anySelected = true
				break
			}
		}
		for i := range m.files {
			m.files[i].Selected = !anySelected
		}

	// ── Deletion ────────────────────────────────────────────────────────────
	case "d":
		selCount := 0
		for _, f := range m.files {
			if f.Selected {
				selCount++
			}
		}
		if selCount > 0 {
			m.viewState = StateConfirm
			m.statusMsg = ""
		} else {
			m.statusMsg = "No files selected — press Space to select files"
		}

	// ── Mode toggles ────────────────────────────────────────────────────────
	case "t":
		m.dryRun = !m.dryRun
		if m.dryRun {
			m.statusMsg = "Dry-run ON — deletions are preview only"
		} else {
			m.statusMsg = "Dry-run OFF — deletions will be permanent!"
		}

	// ── Age filter ──────────────────────────────────────────────────────────
	case "+", "=":
		m.ageValue++
		return m.startScan(false)
	case "-":
		if m.ageValue > 1 {
			m.ageValue--
			return m.startScan(false)
		}
		m.statusMsg = "Minimum age is 1"
	case "u":
		m.ageUnit = (m.ageUnit + 1) % 4
		return m.startScan(false)

	// ── Sort ────────────────────────────────────────────────────────────────
	case "s":
		m.sortBy = (m.sortBy + 1) % 3
		m.applySort()
	case "S":
		m.sortDesc = !m.sortDesc
		m.applySort()

	// ── Other ───────────────────────────────────────────────────────────────
	case "r":
		return m.startScan(true)
	case "i":
		if m.scanPath == "" {
			m.statusMsg = "Set a valid scan path before editing ignore rules"
			return m, nil
		}
		m.viewState = StateIgnoreEditor
		m.ignoreDraft = loadIgnoreFileContents(m.scanPath)
		m.ignoreCursor = len([]rune(m.ignoreDraft))
		m.ignoreStatusMsg = ""
		return m, nil
	case "h", "?":
		m.viewState = StateHelp
	case "q", "esc":
		return m, tea.Quit
	}

	return m, nil
}

// ─── Styles ───────────────────────────────────────────────────────────────────

var (
	clrPrimary = lipgloss.Color("39")  // bright blue
	clrWarning = lipgloss.Color("220") // yellow
	clrDanger  = lipgloss.Color("196") // red
	clrSuccess = lipgloss.Color("82")  // green
	clrSubtle  = lipgloss.Color("250") // light gray for secondary text
	clrWhite   = lipgloss.Color("255")
	clrBlack   = lipgloss.Color("0")
	clrBgSel   = lipgloss.Color("25")
	clrBgCur   = lipgloss.Color("28")
	clrBgCurS  = lipgloss.Color("34")
	clrFieldBg = lipgloss.Color("236")

	styleBase       = lipgloss.NewStyle().Foreground(clrWhite)
	styleBarBg      = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	styleMuted      = lipgloss.NewStyle().Foreground(clrSubtle)
	styleBold       = lipgloss.NewStyle().Bold(true)
	styleKey        = lipgloss.NewStyle().Foreground(clrWarning).Bold(true)
	styleSucc       = lipgloss.NewStyle().Foreground(clrSuccess)
	styleDanger     = lipgloss.NewStyle().Foreground(clrDanger).Bold(true)
	styleBox        = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(clrPrimary).Padding(1, 3)
	styleField      = lipgloss.NewStyle().Foreground(clrWhite).Background(clrFieldBg).Padding(0, 1)
	styleFieldMuted = lipgloss.NewStyle().Foreground(clrSubtle).Background(clrFieldBg).Padding(0, 1)
	styleFieldFocus = lipgloss.NewStyle().Foreground(clrBlack).Background(clrWarning).Bold(true).Padding(0, 1)
)

// ─── View ─────────────────────────────────────────────────────────────────────

func (m Model) View() string {
	if m.width == 0 {
		return "Initializing…"
	}
	switch m.viewState {
	case StateHelp:
		return m.viewHelp()
	case StateConfirm:
		return m.viewConfirm()
	case StateIgnoreEditor:
		return m.viewIgnoreEditor()
	default:
		return m.viewList()
	}
}

func (m Model) viewList() string {
	return strings.Join([]string{
		m.renderHeader(),
		m.renderColHeader(),
		m.renderRows(),
		m.renderFooter(),
		m.renderStatusBar(),
	}, "\n")
}

func (m Model) renderHeader() string {
	var modeStr string
	if m.dryRun {
		modeStr = lipgloss.NewStyle().Foreground(clrWarning).Bold(true).Render("DRY-RUN")
	} else {
		modeStr = lipgloss.NewStyle().Foreground(clrDanger).Bold(true).Render("LIVE MODE")
	}

	sortArrow := "↑"
	if m.sortDesc {
		sortArrow = "↓"
	}

	scanState := styleMuted.Render("READY")
	if m.isScanning {
		scanState = styleSucc.Render("SCANNING")
	}

	title := lipgloss.NewStyle().Bold(true).Foreground(clrPrimary).Render("File Age Analyzer")
	line1 := styleBarBg.Render(fmt.Sprintf("%s  [%s]  age: %d %s  sort: %s%s  %s",
		title, modeStr, m.ageValue, m.ageUnit, m.sortBy, sortArrow, scanState,
	))

	pathW := max(20, m.width-25)
	depthW := 4
	line2 := styleBarBg.Render("  path: ") +
		m.renderInputField(m.pathInput, "enter a directory", m.pathCursor, pathW, m.inputFocus == FocusPath) +
		styleBarBg.Render("  depth: ") +
		m.renderInputField(m.depthInput, "0", m.depthCursor, depthW, m.inputFocus == FocusDepth)

	return strings.Join([]string{line1, line2}, "\n")
}

func (m Model) renderInputField(value, placeholder string, cursor, width int, focused bool) string {
	if width < 1 {
		width = 1
	}

	if focused {
		return styleFieldFocus.Render(padRight(renderInputValue(value, cursor, width), width))
	}

	if value == "" {
		return styleFieldMuted.Render(padRight(truncateText(placeholder, width), width))
	}

	display := truncateText(value, width)
	if strings.ContainsRune(value, filepath.Separator) {
		display = truncatePath(value, width)
	}
	return styleField.Render(padRight(display, width))
}

func renderInputValue(value string, cursor, width int) string {
	if width < 1 {
		width = 1
	}

	runes := []rune(value)
	cursor = min(max(0, cursor), len(runes))

	contentWidth := max(0, width-1)
	start := 0
	if cursor > contentWidth {
		start = cursor - contentWidth
	}
	end := min(len(runes), start+contentWidth)

	before := string(runes[start:cursor])
	after := string(runes[cursor:end])
	return padRight(before+"|"+after, width)
}

func (m Model) renderColHeader() string {
	pathW, sizeW, ageW := m.colWidths()
	row := fmt.Sprintf("     %-*s  %*s  %-*s", pathW, "FILE", sizeW, "SIZE", ageW, "AGE")
	return styleMuted.Render(row)
}

func (m Model) renderRows() string {
	lh := m.listHeight()
	w := m.width

	if len(m.files) == 0 {
		line1 := styleMuted.Render(fmt.Sprintf("  No files older than %d %s found.", m.ageValue, m.ageUnit))
		line2 := styleMuted.Render("  Adjust age, unit, path, or depth and the list will rescan automatically.")

		switch {
		case strings.TrimSpace(m.pathInput) == "":
			line1 = styleMuted.Render("  Enter a directory path to start scanning.")
			line2 = styleMuted.Render("  The path field is focused first so you can type immediately.")
		case !isDirectory(m.pathInput):
			line1 = styleDanger.Render("  Path must be an existing directory.")
			line2 = styleMuted.Render("  Fix the path and scanning will restart automatically.")
		case !isValidDepthInput(m.depthInput):
			line1 = styleDanger.Render("  Depth must be 0 or greater.")
			line2 = styleMuted.Render("  Enter a whole number of directory levels to scan.")
		case m.isScanning:
			line1 = styleMuted.Render(fmt.Sprintf("  Scanning %s…", m.pathInput))
			line2 = styleMuted.Render(fmt.Sprintf("  Looking for files older than %d %s up to depth %d.", m.ageValue, m.ageUnit, m.maxDepth))
		}

		lines := []string{"", line1, line2}
		for len(lines) < lh {
			lines = append(lines, "")
		}
		return strings.Join(lines, "\n")
	}

	pathW, sizeW, ageW := m.colWidths()
	rows := make([]string, 0, lh)

	end := min(m.scrollOffset+lh, len(m.files))
	for i := m.scrollOffset; i < end; i++ {
		f := m.files[i]

		check := " [ ] "
		if f.Selected {
			check = lipgloss.NewStyle().Foreground(clrSuccess).Render(" [✓] ")
		}

		age := f.AgeString
		if len(age) > ageW {
			age = age[:ageW]
		}

		line := fmt.Sprintf("%s%-*s  %*s  %-*s",
			check,
			pathW, truncatePath(displayFilePath(f.Path), pathW),
			sizeW, formatSize(f.Size),
			ageW, age,
		)

		var st lipgloss.Style
		switch {
		case i == m.cursor && f.Selected:
			st = lipgloss.NewStyle().Background(clrBgCurS).Foreground(clrBlack).Bold(true)
		case i == m.cursor:
			st = lipgloss.NewStyle().Background(clrBgCur).Foreground(clrWhite).Bold(true)
		case f.Selected:
			st = lipgloss.NewStyle().Background(clrBgSel).Foreground(clrWhite)
		default:
			st = styleBase
		}

		rows = append(rows, st.Width(w).Render(line))
	}

	for len(rows) < lh {
		rows = append(rows, "")
	}

	return strings.Join(rows, "\n")
}

func (m Model) renderFooter() string {
	total := int64(0)
	selCount, selSize := 0, int64(0)
	for _, f := range m.files {
		total += f.Size
		if f.Selected {
			selCount++
			selSize += f.Size
		}
	}

	pos := "0"
	if len(m.files) > 0 {
		pos = fmt.Sprintf("%d/%d", m.cursor+1, len(m.files))
	}

	parts := []string{
		lipgloss.NewStyle().Foreground(clrPrimary).Render(pos) + " files",
		formatSize(total) + " total",
	}
	if m.isScanning {
		parts = append(parts, styleMuted.Render("scanning…"))
	}
	if selCount > 0 {
		parts = append(parts, styleSucc.Render(fmt.Sprintf("%d selected (%s)", selCount, formatSize(selSize))))
	}
	if m.lastAction != "" {
		parts = append(parts, styleSucc.Render("✓ "+m.lastAction))
	}
	if m.statusMsg != "" {
		parts = append(parts, styleDanger.Render("⚠ "+m.statusMsg))
	}

	return "  " + strings.Join(parts, styleMuted.Render("  │  "))
}

func (m Model) renderStatusBar() string {
	var hints []struct{ k, d string }
	if m.inputFocus == FocusList {
		hints = []struct{ k, d string }{
			{"Tab", "edit path/depth"},
			{"↑↓ / jk", "navigate"},
			{"Space", "select"},
			{"a / A", "all/toggle"},
			{"d", "delete"},
			{"+ / -", "age"},
			{"u", "unit"},
			{"t", "dry-run"},
			{"s / S", "sort/rev"},
			{"i", "ignore rules"},
			{"r", "rescan"},
			{"h / ?", "help"},
			{"q", "quit"},
		}
	} else {
		hints = []struct{ k, d string }{
			{"type", "edit field"},
			{"← →", "move cursor"},
			{"Backspace", "delete"},
			{"Tab", "next"},
			{"Shift+Tab", "prev"},
			{"Enter / Esc", "done"},
			{"Ctrl+C", "quit"},
		}
	}

	parts := make([]string, len(hints))
	for i, h := range hints {
		parts[i] = styleKey.Render(h.k) + " " + h.d
	}
	return styleBarBg.Render(strings.Join(parts, "  "))
}

// ─── Overlay screens ──────────────────────────────────────────────────────────

func (m Model) viewHelp() string {
	type row struct{ k, d string }
	type section struct {
		title string
		rows  []row
	}

	sections := []section{
		{"Inputs", []row{
			{"Tab", "Focus path → depth → list"},
			{"Shift+Tab", "Move focus backward"},
			{"Enter / Esc", "Leave the active input"},
			{"Type while focused", "Edit path or depth and auto-rescan when valid"},
		}},
		{"Navigation", []row{
			{"↑ / k", "Move cursor up"},
			{"↓ / j", "Move cursor down"},
			{"g / Home", "Jump to top"},
			{"G / End", "Jump to bottom"},
			{"Ctrl+D", "Half-page down"},
			{"Ctrl+U", "Half-page up"},
		}},
		{"Selection", []row{
			{"Space / Enter", "Toggle selection"},
			{"a", "Select all"},
			{"A", "Toggle all (deselect if any selected)"},
		}},
		{"Deletion", []row{
			{"d", "Delete selected (asks confirmation)"},
			{"t", "Toggle dry-run ↔ live mode"},
		}},
		{"Age Filter", []row{
			{"+", "Increase age threshold by 1"},
			{"-", "Decrease age threshold by 1"},
			{"u", "Cycle unit: days → weeks → months → years (auto-rescans)"},
		}},
		{"Sorting", []row{
			{"s", "Cycle sort: age → size → name"},
			{"S", "Toggle ascending / descending"},
		}},
		{"Other", []row{
			{"i", "Edit and save " + defaultIgnoreFileName},
			{"r", "Rescan with current settings"},
			{"h / ?", "Show this help"},
			{"q / Esc", "Quit"},
		}},
	}

	var b strings.Builder
	for _, sec := range sections {
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(clrPrimary).Render(sec.title) + "\n")
		for _, r := range sec.rows {
			b.WriteString(fmt.Sprintf("  %-22s %s\n", styleKey.Render(r.k), r.d))
		}
		b.WriteString("\n")
	}

	content := styleBold.Render("Keybindings") + "\n\n" +
		b.String() +
		styleMuted.Render("Press any key to close")

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, styleBox.Render(content))
}

func (m Model) viewConfirm() string {
	selCount, selSize := 0, int64(0)
	for _, f := range m.files {
		if f.Selected {
			selCount++
			selSize += f.Size
		}
	}

	var actionLine string
	if m.dryRun {
		actionLine = lipgloss.NewStyle().Foreground(clrWarning).Bold(true).Render(
			fmt.Sprintf("DRY-RUN: Would delete %d file(s) (%s)", selCount, formatSize(selSize)),
		)
	} else {
		actionLine = styleDanger.Render(
			fmt.Sprintf("PERMANENTLY DELETE %d file(s) (%s)?", selCount, formatSize(selSize)),
		)
	}

	content := actionLine + "\n\n" +
		styleKey.Render("y") + "  Yes, proceed\n" +
		styleKey.Render("n / Esc") + "  Cancel"

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		styleBox.Render(styleBold.Render("Confirm Deletion")+"\n\n"+content))
}

func (m Model) viewIgnoreEditor() string {
	editorWidth := min(max(50, m.width-12), 100)
	editorHeight := min(max(10, m.height-10), 24)
	fileLabel := filepath.Join(m.pathInput, defaultIgnoreFileName)
	body := renderEditorBuffer(m.ignoreDraft, m.ignoreCursor, max(10, editorWidth-8), max(6, editorHeight-8))

	status := styleMuted.Render("Ctrl+S save  Esc close  Enter newline")
	if m.ignoreStatusMsg != "" {
		status = styleDanger.Render(m.ignoreStatusMsg)
	}

	content := styleBold.Render("Edit Ignore Rules") + "\n" +
		styleMuted.Render(fileLabel) + "\n\n" +
		styleFieldFocus.Width(editorWidth-6).Height(max(6, editorHeight-8)).Render(body) + "\n\n" +
		status

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		styleBox.Width(editorWidth).Render(content))
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

func (m Model) listHeight() int {
	chromeHeight := lipgloss.Height(m.renderHeader()) +
		lipgloss.Height(m.renderColHeader()) +
		lipgloss.Height(m.renderFooter()) +
		lipgloss.Height(m.renderStatusBar()) +
		4
	return max(1, m.height-chromeHeight)
}

func (m Model) colWidths() (pathW, sizeW, ageW int) {
	sizeW, ageW = 10, 22
	pathW = max(20, m.width-5-2-sizeW-2-ageW)
	return
}

func (m *Model) applySort() {
	sort.Slice(m.files, func(i, j int) bool {
		var less bool
		switch m.sortBy {
		case SortAge:
			less = m.files[i].ModTime.Before(m.files[j].ModTime)
		case SortSize:
			less = m.files[i].Size < m.files[j].Size
		case SortName:
			less = m.files[i].Path < m.files[j].Path
		}
		if m.sortDesc {
			return !less
		}
		return less
	})
}

func (m *Model) clampScroll() {
	lh := m.listHeight()
	if m.scrollOffset > m.cursor {
		m.scrollOffset = m.cursor
	}
	if m.cursor >= m.scrollOffset+lh {
		m.scrollOffset = m.cursor - lh + 1
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
}

func (m Model) focusedInput() (string, int) {
	switch m.inputFocus {
	case FocusPath:
		return m.pathInput, m.pathCursor
	case FocusDepth:
		return m.depthInput, m.depthCursor
	default:
		return "", 0
	}
}

func (m *Model) setFocusedInput(value string, cursor int) {
	switch m.inputFocus {
	case FocusPath:
		m.pathInput = value
		m.pathCursor = min(max(0, cursor), len([]rune(value)))
	case FocusDepth:
		m.depthInput = value
		m.depthCursor = min(max(0, cursor), len([]rune(value)))
	}
}

func (m Model) startScan(force bool) (tea.Model, tea.Cmd) {
	scanPath, err := resolveDirectory(m.pathInput)
	if err != nil {
		return m.invalidateInputs(scanPathError(m.pathInput)), nil
	}

	maxDepth, err := parseDepthInput(m.depthInput)
	if err != nil {
		return m.invalidateInputs("Depth must be 0 or greater"), nil
	}

	m.scanPath = scanPath
	m.maxDepth = maxDepth

	signature := buildScanSignature(m.scanPath, m.maxDepth, m.ageValue, m.ageUnit, m.extensions)
	if !force && signature == m.lastScanSignature {
		m.statusMsg = ""
		return m, nil
	}

	m.statusMsg, m.lastAction = "", ""
	m.files = nil
	m.cursor, m.scrollOffset = 0, 0
	m.isScanning = true
	m.scanSeq++
	m.activeScanID = m.scanSeq
	m.lastScanSignature = signature

	return m, doScan(m.scanPath, m.ageValue, m.ageUnit, m.maxDepth, m.extensions, m.activeScanID)
}

func (m Model) invalidateInputs(status string) Model {
	m.scanPath = ""
	m.files = nil
	m.cursor, m.scrollOffset = 0, 0
	m.isScanning = false
	m.statusMsg = status
	m.lastAction = ""
	m.lastScanSignature = ""
	m.scanSeq++
	m.activeScanID = m.scanSeq
	return m
}

func buildScanSignature(path string, maxDepth, ageValue int, ageUnit AgeUnit, extensions string) string {
	return fmt.Sprintf("%s|%d|%d|%d|%s", path, maxDepth, ageValue, ageUnit, extensions)
}

func resolveDirectory(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("missing directory")
	}

	expandedPath, err := expandHomePath(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid directory")
	}

	info, err := os.Stat(expandedPath)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("invalid directory")
	}

	return filepath.Clean(expandedPath), nil
}

func expandHomePath(path string) (string, error) {
	if path == "" || path[0] != '~' {
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("missing home directory")
	}

	if path == "~" {
		return home, nil
	}

	next := path[1]
	if next != '/' && next != '\\' {
		return path, nil
	}

	return filepath.Join(home, path[2:]), nil
}

func displayPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}

	cleaned := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return cleaned
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return cleaned
	}

	rel, err := filepath.Rel(filepath.Clean(home), cleaned)
	if err != nil {
		return cleaned
	}

	if rel == "." {
		return "~"
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return cleaned
	}

	return filepath.Join("~", rel)
}

func isDirectory(path string) bool {
	_, err := resolveDirectory(path)
	return err == nil
}

func scanPathError(path string) string {
	if strings.TrimSpace(path) == "" {
		return "Enter a directory path to scan"
	}
	return "Path must be an existing directory"
}

func parseDepthInput(value string) (int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, fmt.Errorf("missing depth")
	}

	depth, err := strconv.Atoi(trimmed)
	if err != nil || depth < 0 {
		return 0, fmt.Errorf("invalid depth")
	}
	return depth, nil
}

func isValidDepthInput(value string) bool {
	_, err := parseDepthInput(value)
	return err == nil
}

func computeThreshold(value int, unit AgeUnit) time.Time {
	now := time.Now()
	switch unit {
	case UnitDays:
		return now.AddDate(0, 0, -value)
	case UnitWeeks:
		return now.AddDate(0, 0, -value*7)
	case UnitMonths:
		return now.AddDate(0, -value, 0)
	default:
		return now.AddDate(-value, 0, 0)
	}
}

// buildSystemDirs returns paths that should never be scanned, per OS.
func buildSystemDirs() map[string]bool {
	dirs := map[string]bool{}
	if runtime.GOOS == "windows" {
		for _, env := range []string{"SystemRoot", "ProgramFiles", "ProgramFiles(x86)", "ProgramData"} {
			if v := os.Getenv(env); v != "" {
				dirs[filepath.Clean(v)] = true
			}
		}
	} else {
		for _, p := range []string{"/proc", "/sys", "/dev", "/run", "/tmp", "/var/run"} {
			dirs[p] = true
		}
	}
	return dirs
}

// isSystemName reports whether a file/dir name should be skipped as a system entry.
func isSystemName(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	if runtime.GOOS == "windows" {
		if strings.HasPrefix(name, "$") {
			return true
		}
		lower := strings.ToLower(name)
		switch lower {
		case "system volume information", "recovery", "boot", "bootmgr",
			"pagefile.sys", "hiberfil.sys", "swapfile.sys":
			return true
		}
	}
	return false
}

func findFiles(rootPath string, threshold time.Time, maxDepth int, extensions string) []FileEntry {
	var files []FileEntry
	var extFilter map[string]bool
	systemDirs := buildSystemDirs()
	ignoreMatcher := loadIgnoreMatcher(rootPath)

	if extensions != "" {
		extFilter = make(map[string]bool)
		for _, ext := range strings.Split(extensions, ",") {
			normalized := strings.ToLower(strings.TrimSpace(ext))
			if normalized == "" {
				continue
			}
			if !strings.HasPrefix(normalized, ".") {
				normalized = "." + normalized
			}
			extFilter[normalized] = true
		}
	}

	filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		isRoot := path == rootPath
		rel, relErr := filepath.Rel(rootPath, path)
		if relErr != nil {
			return nil
		}
		depth := pathDepth(rel, info.IsDir())
		name := filepath.Base(path)

		if !isRoot && ignoreMatcher.matches(rel) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if !isRoot && isSystemName(name) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}

		if info.IsDir() {
			if !isRoot && systemDirs[filepath.Clean(path)] {
				return filepath.SkipDir
			}
			if depth > maxDepth {
				return filepath.SkipDir
			}
			return nil
		}

		if depth > maxDepth {
			return nil
		}
		if extFilter != nil && !extFilter[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		if info.ModTime().Before(threshold) {
			files = append(files, FileEntry{
				Path:      path,
				Size:      info.Size(),
				ModTime:   info.ModTime(),
				AgeString: formatDuration(time.Since(info.ModTime())),
			})
		}
		return nil
	})

	return files
}

func loadIgnoreMatcher(rootPath string) ignoreMatcher {
	data, err := os.ReadFile(filepath.Join(rootPath, defaultIgnoreFileName))
	if err != nil {
		return ignoreMatcher{}
	}

	matcher := ignoreMatcher{}
	for _, line := range strings.Split(string(data), "\n") {
		pattern, ok := compileIgnorePattern(line)
		if ok {
			matcher.patterns = append(matcher.patterns, pattern)
		}
	}

	return matcher
}

func loadIgnoreFileContents(rootPath string) string {
	data, err := os.ReadFile(filepath.Join(rootPath, defaultIgnoreFileName))
	if err != nil {
		return ""
	}
	return normalizeEditorText(string(data))
}

func saveIgnoreFileContents(rootPath, contents string) error {
	path := filepath.Join(rootPath, defaultIgnoreFileName)
	normalized := normalizeEditorText(contents)
	if normalized != "" && !strings.HasSuffix(normalized, "\n") {
		normalized += "\n"
	}
	return os.WriteFile(path, []byte(normalized), 0o644)
}

func compileIgnorePattern(line string) (ignorePattern, bool) {
	pattern := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
	if pattern == "" || strings.HasPrefix(pattern, "#") {
		return ignorePattern{}, false
	}

	anchored := strings.HasPrefix(pattern, "/")
	if anchored {
		pattern = strings.TrimPrefix(pattern, "/")
	}

	dirOnly := strings.HasSuffix(pattern, "/")
	if dirOnly {
		pattern = strings.TrimSuffix(pattern, "/")
	}

	pattern = filepath.ToSlash(pattern)
	pattern = strings.TrimPrefix(pattern, "./")
	if pattern == "" {
		return ignorePattern{}, false
	}

	var expr strings.Builder
	if anchored {
		expr.WriteString("^")
	} else {
		expr.WriteString("(^|.*/)")
	}
	expr.WriteString(globPatternToRegexp(pattern))
	if dirOnly {
		expr.WriteString("(/.*)?$")
	} else {
		expr.WriteString("$")
	}

	return ignorePattern{regex: regexp.MustCompile(expr.String())}, true
}

func globPatternToRegexp(pattern string) string {
	var expr strings.Builder
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		switch ch {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				expr.WriteString(".*")
				i++
			} else {
				expr.WriteString("[^/]*")
			}
		case '?':
			expr.WriteString("[^/]")
		default:
			if strings.ContainsRune(`.+()|[]{}^$\`, rune(ch)) {
				expr.WriteByte('\\')
			}
			expr.WriteByte(ch)
		}
	}
	return expr.String()
}

func (m ignoreMatcher) matches(relPath string) bool {
	if len(m.patterns) == 0 {
		return false
	}

	normalized := filepath.ToSlash(relPath)
	if normalized == "." || normalized == "" {
		return false
	}

	for _, pattern := range m.patterns {
		if pattern.regex.MatchString(normalized) {
			return true
		}
	}

	return false
}

func renderEditorBuffer(value string, cursor, width, height int) string {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}

	runes := []rune(normalizeEditorText(value))
	cursor = min(max(0, cursor), len(runes))
	currentLine := lineIndexAtCursor(runes, cursor)

	lines := renderEditorLines(runes, cursor)
	start := 0
	if currentLine >= height {
		start = currentLine - height + 1
	}
	if start+height > len(lines) {
		start = max(0, len(lines)-height)
	}

	visible := lines[start:min(len(lines), start+height)]
	for i, line := range visible {
		visible[i] = padRight(truncateText(line, width), width)
	}
	for len(visible) < height {
		visible = append(visible, strings.Repeat(" ", width))
	}

	return strings.Join(visible, "\n")
}

func renderEditorLines(runes []rune, cursor int) []string {
	lines := []string{}
	line := make([]rune, 0)
	for i := 0; i <= len(runes); i++ {
		if i == cursor {
			line = append(line, '|')
		}
		if i == len(runes) {
			lines = append(lines, string(line))
			break
		}
		if runes[i] == '\n' {
			lines = append(lines, string(line))
			line = make([]rune, 0)
			continue
		}
		line = append(line, runes[i])
	}
	if len(lines) == 0 {
		return []string{"|"}
	}
	return lines
}

func lineIndexAtCursor(runes []rune, cursor int) int {
	cursor = min(max(0, cursor), len(runes))
	line := 0
	for i := 0; i < cursor; i++ {
		if runes[i] == '\n' {
			line++
		}
	}
	return line
}

func normalizeEditorText(value string) string {
	return strings.ReplaceAll(value, "\r\n", "\n")
}

func insertTextAtCursor(value string, cursor int, insert string) (string, int) {
	runes := []rune(normalizeEditorText(value))
	insertRunes := []rune(normalizeEditorText(insert))
	cursor = min(max(0, cursor), len(runes))
	updated := append(runes[:cursor], append(insertRunes, runes[cursor:]...)...)
	return string(updated), cursor + len(insertRunes)
}

func deleteBeforeCursor(value string, cursor int) (string, int) {
	runes := []rune(normalizeEditorText(value))
	cursor = min(max(0, cursor), len(runes))
	if cursor == 0 {
		return value, cursor
	}
	updated := append(runes[:cursor-1], runes[cursor:]...)
	return string(updated), cursor - 1
}

func deleteAtCursor(value string, cursor int) (string, int) {
	runes := []rune(normalizeEditorText(value))
	cursor = min(max(0, cursor), len(runes))
	if cursor >= len(runes) {
		return value, cursor
	}
	updated := append(runes[:cursor], runes[cursor+1:]...)
	return string(updated), cursor
}

func moveCursorVertical(value string, cursor, delta int) int {
	runes := []rune(normalizeEditorText(value))
	cursor = min(max(0, cursor), len(runes))
	start, end := currentLineBounds(runes, cursor)
	column := cursor - start

	if delta < 0 {
		if start == 0 {
			return cursor
		}
		prevEnd := start - 1
		prevStart, _ := currentLineBounds(runes, prevEnd)
		return prevStart + min(column, prevEnd-prevStart)
	}
	if delta > 0 {
		if end >= len(runes) {
			return cursor
		}
		nextStart := end + 1
		_, nextEnd := currentLineBounds(runes, nextStart)
		return nextStart + min(column, nextEnd-nextStart)
	}

	return cursor
}

func currentLineBounds(runes []rune, cursor int) (int, int) {
	cursor = min(max(0, cursor), len(runes))
	start := cursor
	for start > 0 && runes[start-1] != '\n' {
		start--
	}
	end := cursor
	for end < len(runes) && runes[end] != '\n' {
		end++
	}
	return start, end
}

func pathDepth(rel string, isDir bool) int {
	if rel == "." {
		return 0
	}

	depth := strings.Count(rel, string(filepath.Separator))
	if isDir {
		return depth + 1
	}
	return depth
}

func formatSize(bytes int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	size := float64(bytes)
	idx := 0
	for size >= 1024 && idx < len(units)-1 {
		size /= 1024
		idx++
	}
	if idx == 0 {
		return fmt.Sprintf("%d B", int64(size))
	}
	return fmt.Sprintf("%.1f %s", size, units[idx])
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours() / 24)
	years := days / 365
	months := days / 30
	switch {
	case years >= 2:
		if mo := (days - years*365) / 30; mo > 0 {
			return fmt.Sprintf("%dy %dmo ago", years, mo)
		}
		return fmt.Sprintf("%d years ago", years)
	case years == 1:
		if mo := (days - 365) / 30; mo > 0 {
			return fmt.Sprintf("1y %dmo ago", mo)
		}
		return "1 year ago"
	case months > 0:
		return fmt.Sprintf("%d months ago", months)
	case days > 0:
		return fmt.Sprintf("%d days ago", days)
	default:
		return "today"
	}
}

func displayFilePath(fullPath string) string {
	return displayPath(fullPath)
}

func truncatePath(path string, maxLen int) string {
	runes := []rune(path)
	if maxLen <= 1 || len(runes) <= maxLen {
		return path
	}
	if maxLen == 2 {
		return string(runes[:1]) + "…"
	}

	prefixLen := max(1, (maxLen-1)/2)
	suffixLen := maxLen - prefixLen - 1
	return string(runes[:prefixLen]) + "…" + string(runes[len(runes)-suffixLen:])
}

func truncateText(value string, maxLen int) string {
	runes := []rune(value)
	if maxLen <= 1 || len(runes) <= maxLen {
		return value
	}
	if maxLen == 2 {
		return string(runes[:1]) + "…"
	}
	return string(runes[:maxLen-1]) + "…"
}

func padRight(value string, width int) string {
	runes := []rune(value)
	if len(runes) >= width {
		return string(runes[:width])
	}
	return value + strings.Repeat(" ", width-len(runes))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func main() {
	scanPath := flag.String("path", "", "Initial directory to scan")
	ageValue := flag.Int("age", 1, "Age threshold (number, paired with -unit)")
	ageUnitStr := flag.String("unit", "years", "Age unit: days, weeks, months, years")
	dryRun := flag.Bool("dry-run", true, "Dry-run — preview deletions without acting (safe default)")
	extensions := flag.String("ext", "", "Filter by extensions, comma-separated (e.g. log,tmp,bak)")
	depth := flag.Int("depth", 3, "Maximum directory depth to scan (0 = current directory only)")

	flag.Parse()

	var ageUnit AgeUnit
	switch strings.ToLower(strings.TrimSpace(*ageUnitStr)) {
	case "day", "days", "d":
		ageUnit = UnitDays
	case "week", "weeks", "w":
		ageUnit = UnitWeeks
	case "month", "months", "m":
		ageUnit = UnitMonths
	default:
		ageUnit = UnitYears
	}

	rawPath := strings.TrimSpace(*scanPath)
	rawDepth := strconv.Itoa(*depth)
	resolvedPath, pathErr := resolveDirectory(rawPath)

	m := Model{
		pathInput:   rawPath,
		pathCursor:  len([]rune(rawPath)),
		ageValue:    *ageValue,
		ageUnit:     ageUnit,
		dryRun:      *dryRun,
		extensions:  *extensions,
		depthInput:  rawDepth,
		depthCursor: len([]rune(rawDepth)),
		viewState:   StateList,
		inputFocus:  FocusList,
		width:       80,
		height:      24,
	}

	switch {
	case rawPath == "":
		m.inputFocus = FocusPath
		m.statusMsg = "Enter a directory path to scan"
	case pathErr != nil:
		m.inputFocus = FocusPath
		m.statusMsg = "Path must be an existing directory"
	case *depth < 0:
		m.inputFocus = FocusDepth
		m.statusMsg = "Depth must be 0 or greater"
	default:
		m.scanPath = resolvedPath
		m.maxDepth = *depth
		m.isScanning = true
		m.scanSeq = 1
		m.activeScanID = 1
		m.lastScanSignature = buildScanSignature(m.scanPath, m.maxDepth, m.ageValue, m.ageUnit, m.extensions)
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

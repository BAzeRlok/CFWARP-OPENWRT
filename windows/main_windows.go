//go:build windows

package main

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	win "golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

const (
	version = "4.2.0-r5"

	wsOverlappedWindow = 0x00CF0000
	wsChild            = 0x40000000
	wsVisible          = 0x10000000
	wsTabStop          = 0x00010000
	wsBorder           = 0x00800000
	wsVScroll          = 0x00200000
	wsClipChildren     = 0x02000000

	esLeft        = 0x0000
	esMultiline   = 0x0004
	esAutoVScroll = 0x0040
	esReadOnly    = 0x0800

	bsPushButton    = 0x00000000
	bsDefPushButton = 0x00000001
	bsAutoCheckBox  = 0x00000003
	cbsDropDownList = 0x00000003
	bmGetCheck      = 0x00F0
	bmSetCheck      = 0x00F1
	cbAddString     = 0x0143
	cbGetCurSel     = 0x0147
	cbSetCurSel     = 0x014E

	swShowNormal = 1
	swShow       = 5

	wmCreate      = 0x0001
	wmSize        = 0x0005
	wmClose       = 0x0010
	wmDestroy     = 0x0002
	wmCommand     = 0x0111
	wmSetFont     = 0x0030
	wmAppTaskDone = 0x8001

	colorBtnFace   = 15
	defaultGUIFont = 17
	idcArrow       = 32512
	idiApplication = 32512

	mbOK              = 0x00000000
	mbYesNo           = 0x00000004
	mbIconInformation = 0x00000040
	mbIconWarning     = 0x00000030
	mbIconError       = 0x00000010
	idYes             = 6

	idInstall = 1001 + iota
	idRescan
	idConnect
	idDisconnect
	idReconnect
	idStatus
	idUninstall
	idTerms
	idPrivacy
	idDNSEnabled
	idDNSType
	idApplyDNS
	idDNSOnly
)

//go:embed warp.ps1
var warpScript []byte

//go:embed dns_only.ps1
var dnsOnlyScript []byte

type point struct {
	x int32
	y int32
}

type message struct {
	hwnd     uintptr
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       point
	lPrivate uint32
}

type rect struct {
	left   int32
	top    int32
	right  int32
	bottom int32
}

type wndClassEx struct {
	cbSize     uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   uintptr
	icon       uintptr
	cursor     uintptr
	background uintptr
	menuName   *uint16
	className  *uint16
	iconSmall  uintptr
}

type taskResult struct {
	action string
	output string
	err    error
}

type application struct {
	window            uintptr
	statusLabel       uintptr
	logEdit           uintptr
	sampleEdit        uintptr
	countriesEdit     uintptr
	nodesEdit         uintptr
	excludedCountries uintptr
	excludedNodes     uintptr
	dnsEnabled        uintptr
	dnsTypeLabel      uintptr
	dnsType           uintptr
	dnsAddressLabel   uintptr
	dnsAddress        uintptr
	dnsApplyButton    uintptr
	dnsOnlyButton     uintptr
	dnsLoadError      error
	actionButtons     []uintptr
	allControls       []uintptr
	busy              bool
	termsAccepted     bool
	resultMu          sync.Mutex
	result            taskResult
}

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")

	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procEnableWindow        = user32.NewProc("EnableWindow")
	procGetClientRect       = user32.NewProc("GetClientRect")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procGetWindowTextLength = user32.NewProc("GetWindowTextLengthW")
	procGetWindowTextW      = user32.NewProc("GetWindowTextW")
	procLoadCursorW         = user32.NewProc("LoadCursorW")
	procLoadIconW           = user32.NewProc("LoadIconW")
	procMessageBoxW         = user32.NewProc("MessageBoxW")
	procMoveWindow          = user32.NewProc("MoveWindow")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procSendMessageW        = user32.NewProc("SendMessageW")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procSetProcessDPIAware  = user32.NewProc("SetProcessDPIAware")
	procSetWindowTextW      = user32.NewProc("SetWindowTextW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procUpdateWindow        = user32.NewProc("UpdateWindow")

	procGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
	procSetConsoleCP       = kernel32.NewProc("SetConsoleCP")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
	procGetStockObject     = gdi32.NewProc("GetStockObject")
	procIsUserAnAdmin      = shell32.NewProc("IsUserAnAdmin")
	procShellExecuteW      = shell32.NewProc("ShellExecuteW")

	app *application
)

var actionTitles = map[string]string{
	"Install":     "установка",
	"Rescan":      "поиск лучшего endpoint",
	"Connect":     "подключение",
	"Disconnect":  "отключение",
	"Reconnect":   "переподключение",
	"Status":      "проверка состояния",
	"Uninstall":   "удаление",
	"ApplyDNS":    "применение DNS",
	"DNSOnly":     "только DNS",
	"StopDNSOnly": "отключение DNS",
}

func actionTitle(action string) string {
	if title, ok := actionTitles[action]; ok {
		return title
	}
	return action
}

func utf16(value string) *uint16 {
	pointer, _ := syscall.UTF16PtrFromString(value)
	return pointer
}

func lowWord(value uintptr) uint16 {
	return uint16(value & 0xffff)
}

func messageBox(owner uintptr, text, title string, flags uintptr) int {
	result, _, _ := procMessageBoxW.Call(
		owner,
		uintptr(unsafe.Pointer(utf16(text))),
		uintptr(unsafe.Pointer(utf16(title))),
		flags,
	)
	return int(result)
}

func isAdministrator() bool {
	result, _, _ := procIsUserAnAdmin.Call()
	return result != 0
}

func shellExecute(owner uintptr, verb, target, parameters string) error {
	result, _, callErr := procShellExecuteW.Call(
		owner,
		uintptr(unsafe.Pointer(utf16(verb))),
		uintptr(unsafe.Pointer(utf16(target))),
		uintptr(unsafe.Pointer(utf16(parameters))),
		0,
		swShowNormal,
	)
	if result <= 32 {
		return fmt.Errorf("ShellExecute завершился с кодом %d: %v", result, callErr)
	}
	return nil
}

func elevate() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	quoted := make([]string, 0, len(os.Args)-1)
	for _, argument := range os.Args[1:] {
		quoted = append(quoted, syscall.EscapeArg(argument))
	}
	return shellExecute(0, "runas", executable, strings.Join(quoted, " "))
}

func openURL(url string) {
	if err := shellExecute(app.window, "open", url, ""); err != nil {
		messageBox(app.window, err.Error(), "WARP", mbOK|mbIconError)
	}
}

func setWindowText(handle uintptr, value string) {
	procSetWindowTextW.Call(handle, uintptr(unsafe.Pointer(utf16(value))))
}

func windowText(handle uintptr) string {
	length, _, _ := procGetWindowTextLength.Call(handle)
	buffer := make([]uint16, int(length)+1)
	procGetWindowTextW.Call(handle, uintptr(unsafe.Pointer(&buffer[0])), length+1)
	return syscall.UTF16ToString(buffer)
}

func moveWindow(handle uintptr, x, y, width, height int32) {
	if handle != 0 {
		procMoveWindow.Call(handle, uintptr(x), uintptr(y), uintptr(width), uintptr(height), 1)
	}
}

func createControl(class, text string, style uint32, id int) uintptr {
	handle, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(utf16(class))),
		uintptr(unsafe.Pointer(utf16(text))),
		uintptr(style),
		0, 0, 100, 30,
		app.window,
		uintptr(id),
		0,
		0,
	)
	if handle == 0 {
		return 0
	}
	font, _, _ := procGetStockObject.Call(defaultGUIFont)
	procSendMessageW.Call(handle, wmSetFont, font, 1)
	app.allControls = append(app.allControls, handle)
	return handle
}

func createLabel(text string) uintptr {
	return createControl("STATIC", text, wsChild|wsVisible, 0)
}

func createEdit(text string) uintptr {
	return createControl("EDIT", text, wsChild|wsVisible|wsTabStop|wsBorder|esLeft, 0)
}

func createButton(text string, id int, primary bool) uintptr {
	style := uint32(wsChild | wsVisible | wsTabStop | bsPushButton)
	if primary {
		style = wsChild | wsVisible | wsTabStop | bsDefPushButton
	}
	button := createControl("BUTTON", text, style, id)
	app.actionButtons = append(app.actionButtons, button)
	return button
}

func layoutControls(width, height int32) {
	if app == nil || len(app.allControls) < 22 {
		return
	}
	margin := int32(18)
	contentWidth := width - 2*margin
	if contentWidth < 620 {
		contentWidth = 620
	}

	moveWindow(app.allControls[0], margin, 15, contentWidth, 28)
	moveWindow(app.statusLabel, margin, 47, contentWidth, 22)

	buttonY := int32(78)
	buttonWidth := int32(112)
	gap := int32(8)
	for index, button := range app.actionButtons[:7] {
		x := margin + int32(index)*(buttonWidth+gap)
		moveWindow(button, x, buttonY, buttonWidth, 32)
	}

	settingsY := int32(128)
	labelWidth := int32(165)
	fieldWidth := (contentWidth - 2*labelWidth - 30) / 2
	if fieldWidth < 150 {
		fieldWidth = 150
	}
	leftX := margin
	leftField := leftX + labelWidth
	rightX := margin + contentWidth/2 + 8
	rightField := rightX + labelWidth

	moveWindow(app.allControls[9], leftX, settingsY, labelWidth-8, 23)
	moveWindow(app.sampleEdit, leftField, settingsY-3, 70, 27)
	moveWindow(app.allControls[11], rightX, settingsY, labelWidth-8, 23)
	moveWindow(app.countriesEdit, rightField, settingsY-3, fieldWidth, 27)

	settingsY += 37
	moveWindow(app.allControls[13], leftX, settingsY, labelWidth-8, 23)
	moveWindow(app.nodesEdit, leftField, settingsY-3, fieldWidth, 27)
	moveWindow(app.allControls[15], rightX, settingsY, labelWidth-8, 23)
	moveWindow(app.excludedCountries, rightField, settingsY-3, fieldWidth, 27)

	settingsY += 37
	moveWindow(app.allControls[17], leftX, settingsY, labelWidth-8, 23)
	moveWindow(app.excludedNodes, leftField, settingsY-3, fieldWidth, 27)
	moveWindow(app.allControls[19], rightX, settingsY-3, 145, 27)
	moveWindow(app.allControls[20], rightX+153, settingsY-3, 175, 27)

	settingsY += 37
	moveWindow(app.dnsEnabled, leftX, settingsY-3, labelWidth, 27)
	moveWindow(app.dnsTypeLabel, leftField, settingsY, 42, 23)
	moveWindow(app.dnsType, leftField+45, settingsY-3, 100, 140)
	moveWindow(app.dnsOnlyButton, rightX, settingsY-3, labelWidth-8, 27)
	moveWindow(app.dnsApplyButton, rightField, settingsY-3, fieldWidth, 27)
	settingsY += 37
	moveWindow(app.dnsAddressLabel, leftX, settingsY, labelWidth-8, 23)
	moveWindow(app.dnsAddress, leftField, settingsY-3, contentWidth-labelWidth, 27)

	logY := settingsY + 43
	moveWindow(app.allControls[21], margin, logY, contentWidth, 23)
	logY += 25
	logHeight := height - logY - margin
	if logHeight < 150 {
		logHeight = 150
	}
	moveWindow(app.logEdit, margin, logY, contentWidth, logHeight)
}

func boolToUintptr(value bool) uintptr {
	if value {
		return 1
	}
	return 0
}

func setBusy(busy bool, status string) {
	app.busy = busy
	for _, button := range app.actionButtons {
		procEnableWindow.Call(button, boolToUintptr(!busy))
	}
	for _, control := range []uintptr{app.sampleEdit, app.countriesEdit, app.nodesEdit,
		app.excludedCountries, app.excludedNodes, app.dnsEnabled} {
		procEnableWindow.Call(control, boolToUintptr(!busy))
	}
	updateDNSControls()
	setWindowText(app.statusLabel, status)
}

func dnsControlsConfig() dnsConfig {
	checked, _, _ := procSendMessageW.Call(app.dnsEnabled, bmGetCheck, 0, 0)
	selection, _, _ := procSendMessageW.Call(app.dnsType, cbGetCurSel, 0, 0)
	protocol := defaultDNSType
	switch selection {
	case 0:
		protocol = "udp"
	case 1:
		protocol = "dot"
	case 2:
		protocol = "doh"
	}
	return dnsConfig{Enabled: checked == 1, Type: protocol, Address: strings.TrimSpace(windowText(app.dnsAddress))}
}

func setDNSControls(config dnsConfig) {
	procSendMessageW.Call(app.dnsEnabled, bmSetCheck, boolToUintptr(config.Enabled), 0)
	selection := uintptr(2)
	switch config.Type {
	case "udp":
		selection = 0
	case "dot":
		selection = 1
	}
	procSendMessageW.Call(app.dnsType, cbSetCurSel, selection, 0)
	setWindowText(app.dnsAddress, config.Address)
	title := "Только DNS"
	if dnsOnlyActive() {
		title = "Выключить DNS"
	}
	setWindowText(app.dnsOnlyButton, title)
	updateDNSControls()
}

func updateDNSControls() {
	checked, _, _ := procSendMessageW.Call(app.dnsEnabled, bmGetCheck, 0, 0)
	enabled := !app.busy && checked == 1
	for _, control := range []uintptr{app.dnsTypeLabel, app.dnsType, app.dnsAddressLabel, app.dnsAddress} {
		procEnableWindow.Call(control, boolToUintptr(enabled))
	}
}

func confirmTerms() bool {
	if app.termsAccepted {
		return true
	}
	text := "Для регистрации WARP необходимо принять условия Cloudflare.\r\n\r\n" +
		"Условия: https://www.cloudflare.com/application/terms/\r\n" +
		"Конфиденциальность: https://www.cloudflare.com/privacypolicy/\r\n\r\n" +
		"Вы прочитали документы и согласны продолжить?"
	if messageBox(app.window, text, "Регистрация Cloudflare WARP", mbYesNo|mbIconInformation) != idYes {
		return false
	}
	app.termsAccepted = true
	return true
}

func scanArguments(action string) ([]string, error) {
	sampleText := strings.TrimSpace(windowText(app.sampleEdit))
	sample, err := strconv.Atoi(sampleText)
	if err != nil || sample < 1 || sample > 16 {
		return nil, errors.New("число кандидатов должно быть от 1 до 16")
	}
	arguments := []string{"-Action", action, "-AcceptCloudflareTerms", "-Sample", strconv.Itoa(sample)}
	optional := []struct {
		name   string
		handle uintptr
	}{
		{"-RequiredCountries", app.countriesEdit},
		{"-RequiredNodes", app.nodesEdit},
		{"-ExcludedCountries", app.excludedCountries},
		{"-ExcludedNodes", app.excludedNodes},
	}
	for _, option := range optional {
		value := strings.TrimSpace(windowText(option.handle))
		if value != "" {
			arguments = append(arguments, option.name, value)
		}
	}
	return arguments, nil
}

func startAction(action string) {
	if app.busy {
		return
	}
	if app.dnsLoadError != nil && (action == "Install" || action == "Rescan") {
		messageBox(app.window, "Не удалось прочитать сохранённые настройки DNS:\r\n"+app.dnsLoadError.Error()+
			"\r\n\r\nПроверьте поля DNS и нажмите «Применить DNS», чтобы явно заменить настройки.", "Настройки DNS", mbOK|mbIconError)
		return
	}
	if (action == "Install" || action == "Rescan") && !confirmTerms() {
		return
	}
	if action == "Uninstall" && messageBox(app.window, "Полностью удалить WARP и AmneziaWG, все их туннели, профили, ключи, настройки и кэши?\r\n\r\nДругие профили AmneziaWG также будут удалены. После завершения окно закроется и этот EXE будет удалён.", "Полное удаление WARP", mbYesNo|mbIconWarning) != idYes {
		return
	}

	arguments := []string{"-Action", action}
	var err error
	if action == "Install" || action == "Rescan" {
		arguments, err = scanArguments(action)
		if err != nil {
			messageBox(app.window, err.Error(), "Некорректные параметры", mbOK|mbIconWarning)
			return
		}
	}
	dns := dnsControlsConfig()
	if action == "DNSOnly" {
		dns.Enabled = true
	}
	if action == "Install" || action == "Rescan" || action == "ApplyDNS" || action == "DNSOnly" {
		if err := validateDNSConfig(dns); err != nil {
			messageBox(app.window, err.Error(), "Некорректные настройки DNS", mbOK|mbIconWarning)
			return
		}
	}
	setWindowText(app.logEdit, "")
	setBusy(true, "Выполняется: "+actionTitle(action)+"…")
	go func() {
		output, runErr := runClientAction(action, arguments, dns)
		app.resultMu.Lock()
		app.result = taskResult{action: action, output: output, err: runErr}
		app.resultMu.Unlock()
		procPostMessageW.Call(app.window, wmAppTaskDone, 0, 0)
	}()
}

func completeAction() {
	app.resultMu.Lock()
	result := app.result
	app.result = taskResult{}
	app.resultMu.Unlock()
	output := strings.TrimSpace(result.output)
	if result.err != nil && !strings.Contains(output, result.err.Error()) {
		output += "\r\n\r\nОшибка: " + result.err.Error()
	}
	if app.dnsLoadError != nil && result.action == "Status" {
		output += "\r\n\r\nОшибка чтения настроек DNS: " + app.dnsLoadError.Error()
	}
	if output == "" {
		output = "Команда не вернула текстовый результат."
	}
	setWindowText(app.logEdit, output)
	config, loadErr := loadDNSConfig()
	app.dnsLoadError = loadErr
	if loadErr == nil {
		setDNSControls(config)
	}
	if result.err != nil {
		setBusy(false, "Ошибка: "+actionTitle(result.action))
		messageBox(app.window, "Операция завершилась ошибкой. Подробности находятся в журнале.", "WARP", mbOK|mbIconError)
		return
	}
	if result.action == "Uninstall" {
		setBusy(false, "Удаление завершено")
		messageBox(app.window, output+"\r\n\r\nОкно закроется. EXE клиента будет удалён после выхода.", "WARP удалён", mbOK|mbIconInformation)
		if err := scheduleLauncherRemoval(); err != nil {
			messageBox(app.window, "Системные компоненты удалены. Не удалось автоматически удалить EXE:\r\n"+err.Error()+"\r\nУдалите скачанный файл вручную после закрытия окна.", "Удаление EXE", mbOK|mbIconWarning)
		}
		procDestroyWindow.Call(app.window)
		return
	}
	if loadErr != nil {
		setWindowText(app.logEdit, output+"\r\n\r\nОшибка чтения настроек DNS: "+loadErr.Error())
		setBusy(false, "Ошибка чтения настроек DNS")
		return
	}
	setBusy(false, "Готово: "+actionTitle(result.action))
}

func runPowerShell(arguments []string) (string, error) {
	for index, argument := range arguments {
		if argument == "-Action" && index+1 < len(arguments) &&
			(arguments[index+1] == "Install" || arguments[index+1] == "Rescan") {
			hash, err := installBundledHelper()
			if err != nil {
				return "", fmt.Errorf("подготовка встроенного компонента WARP: %w", err)
			}
			arguments = append(arguments, "-BundledHelperHash", hash)
			break
		}
	}
	temporaryDirectory, err := os.MkdirTemp("", "warp-windows-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temporaryDirectory)
	scriptPath := filepath.Join(temporaryDirectory, "warp.ps1")
	// Windows PowerShell 5.1 needs a UTF-8 BOM to read Cyrillic source text.
	script := warpScript
	if !bytes.HasPrefix(script, []byte{0xef, 0xbb, 0xbf}) {
		script = append([]byte{0xef, 0xbb, 0xbf}, script...)
	}
	if err := os.WriteFile(scriptPath, script, 0600); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(temporaryDirectory, "dns_only.ps1"), append([]byte{0xef, 0xbb, 0xbf}, dnsOnlyScript...), 0600); err != nil {
		return "", err
	}
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	powerShell := filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	commandArguments := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath}
	commandArguments = append(commandArguments, arguments...)
	command := exec.Command(powerShell, commandArguments...)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err = command.Run()
	if err != nil {
		if strings.Contains(output.String(), "every endpoint was excluded") {
			output.WriteString("\r\nРегистрация успешна. Все найденные узлы исключены заданными фильтрами. Очистите поля «Исключить страны» и «Исключить узлы» либо измените требования и повторите поиск. Иностранный выход без доступной иностранной точки не гарантируется.\r\n")
		}
		output.WriteString("\r\n\r\nОшибка запуска: ")
		output.WriteString(err.Error())
	}
	return output.String(), err
}

func windowProcedure(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmCreate:
		return 0
	case wmSize:
		if app != nil {
			layoutControls(int32(lowWord(lParam)), int32(uint16((lParam>>16)&0xffff)))
		}
		return 0
	case wmCommand:
		switch int(lowWord(wParam)) {
		case idInstall:
			startAction("Install")
		case idRescan:
			startAction("Rescan")
		case idConnect:
			startAction("Connect")
		case idDisconnect:
			startAction("Disconnect")
		case idReconnect:
			startAction("Reconnect")
		case idStatus:
			startAction("Status")
		case idUninstall:
			startAction("Uninstall")
		case idDNSEnabled:
			updateDNSControls()
		case idApplyDNS:
			startAction("ApplyDNS")
		case idDNSOnly:
			if dnsOnlyActive() {
				startAction("StopDNSOnly")
			} else {
				startAction("DNSOnly")
			}
		case idTerms:
			openURL("https://www.cloudflare.com/application/terms/")
		case idPrivacy:
			openURL("https://www.cloudflare.com/privacypolicy/")
		}
		return 0
	case wmAppTaskDone:
		completeAction()
		return 0
	case wmClose:
		if app != nil && app.busy {
			messageBox(hwnd, "Дождитесь завершения текущей операции.", "WARP", mbOK|mbIconInformation)
			return 0
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	result, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return result
}

func createApplicationWindow() error {
	instance, _, _ := procGetModuleHandleW.Call(0)
	className := utf16("CFWarpWindowsClient")
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	icon, _, _ := procLoadIconW.Call(0, idiApplication)
	class := wndClassEx{
		cbSize:     uint32(unsafe.Sizeof(wndClassEx{})),
		wndProc:    syscall.NewCallback(windowProcedure),
		instance:   instance,
		icon:       icon,
		cursor:     cursor,
		background: colorBtnFace + 1,
		className:  className,
		iconSmall:  icon,
	}
	if registered, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&class))); registered == 0 {
		return fmt.Errorf("не удалось зарегистрировать окно: %v", callErr)
	}

	window, _, callErr := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16("WARP for Windows "+version))),
		wsOverlappedWindow|wsClipChildren,
		100, 80, 900, 725,
		0, 0, instance, 0,
	)
	if window == 0 {
		return fmt.Errorf("не удалось создать окно: %v", callErr)
	}
	app = &application{window: window}

	createLabel("Cloudflare WARP — full-tunnel через AmneziaWG")
	app.statusLabel = createLabel("Состояние ещё не проверено")
	createButton("Установить", idInstall, true)
	createButton("Лучший endpoint", idRescan, false)
	createButton("Подключить", idConnect, false)
	createButton("Отключить", idDisconnect, false)
	createButton("Переподключить", idReconnect, false)
	createButton("Состояние", idStatus, false)
	createButton("Удалить", idUninstall, false)
	createLabel("Кандидатов (1–16):")
	app.sampleEdit = createEdit("3")
	createLabel("Требуемые страны:")
	app.countriesEdit = createEdit("")
	createLabel("Требуемые узлы:")
	app.nodesEdit = createEdit("")
	createLabel("Исключить страны:")
	app.excludedCountries = createEdit("")
	createLabel("Исключить узлы:")
	app.excludedNodes = createEdit("")
	createControl("BUTTON", "Условия Cloudflare", wsChild|wsVisible|wsTabStop|bsPushButton, idTerms)
	createControl("BUTTON", "Конфиденциальность", wsChild|wsVisible|wsTabStop|bsPushButton, idPrivacy)
	createLabel("Журнал:")
	app.logEdit = createControl("EDIT", "", wsChild|wsVisible|wsBorder|wsVScroll|esLeft|esMultiline|esAutoVScroll|esReadOnly, 0)
	app.dnsEnabled = createControl("BUTTON", "Сторонний DNS", wsChild|wsVisible|wsTabStop|bsAutoCheckBox, idDNSEnabled)
	app.dnsTypeLabel = createLabel("Тип:")
	app.dnsType = createControl("COMBOBOX", "", wsChild|wsVisible|wsTabStop|wsVScroll|cbsDropDownList, idDNSType)
	for _, protocol := range []string{"UDP", "DoT", "DoH"} {
		procSendMessageW.Call(app.dnsType, cbAddString, 0, uintptr(unsafe.Pointer(utf16(protocol))))
	}
	app.dnsAddressLabel = createLabel("Адрес DNS:")
	app.dnsAddress = createEdit(defaultDNSURL)
	app.dnsApplyButton = createButton("Применить DNS", idApplyDNS, false)
	app.dnsOnlyButton = createButton("Только DNS", idDNSOnly, false)
	dns, dnsErr := loadDNSConfig()
	app.dnsLoadError = dnsErr
	if dnsErr != nil {
		dns = defaultDNSConfig()
	}
	setDNSControls(dns)

	var client rect
	procGetClientRect.Call(window, uintptr(unsafe.Pointer(&client)))
	layoutControls(client.right-client.left, client.bottom-client.top)
	procShowWindow.Call(window, swShow)
	procUpdateWindow.Call(window)
	procSetForegroundWindow.Call(window)
	if dnsErr != nil {
		messageBox(window, "Не удалось прочитать сохранённые настройки DNS:\r\n"+dnsErr.Error()+
			"\r\n\r\nНастройки на диске не изменены. Исправьте поля и нажмите «Применить DNS».", "Настройки DNS", mbOK|mbIconError)
	}
	return nil
}

func messageLoop() int {
	var msg message
	for {
		result, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) == -1 {
			return 1
		}
		if result == 0 {
			return int(msg.wParam)
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func main() {
	service, err := svc.IsWindowsService()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Не удалось определить режим запуска:", err)
		os.Exit(1)
	}
	if service {
		if err := runDNSService(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	runtime.LockOSThread()
	procSetConsoleCP.Call(65001)
	procSetConsoleOutputCP.Call(65001)
	procSetProcessDPIAware.Call()
	if !isAdministrator() {
		if err := elevate(); err != nil {
			messageBox(0, "Не удалось запросить права администратора:\r\n"+err.Error(), "WARP", mbOK|mbIconError)
		}
		return
	}
	// A second GUI must not overwrite the original DNS recovery snapshot
	// while another instance is changing network settings.
	lock, err := win.CreateMutex(nil, false, win.StringToUTF16Ptr(`Global\WARPClientGUI`))
	if errors.Is(err, win.ERROR_ALREADY_EXISTS) {
		win.CloseHandle(lock)
		messageBox(0, "Окно WARP уже открыто. Используйте запущенный экземпляр клиента.", "WARP", mbOK|mbIconInformation)
		return
	}
	if err != nil {
		messageBox(0, "Не удалось защитить настройки от одновременного изменения:\r\n"+err.Error(), "WARP", mbOK|mbIconError)
		return
	}
	defer win.CloseHandle(lock)
	if err := createApplicationWindow(); err != nil {
		messageBox(0, err.Error(), "WARP", mbOK|mbIconError)
		return
	}
	startAction("Status")
	os.Exit(messageLoop())
}

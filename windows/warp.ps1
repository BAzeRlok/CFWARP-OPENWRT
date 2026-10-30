[CmdletBinding()]
param(
    [ValidateSet('Install', 'Connect', 'Disconnect', 'Reconnect', 'Rescan', 'ApplyDNS', 'Status', 'Uninstall', 'PrepareDNSOnly', 'ApplyDNSOnly', 'StopDNSOnly')]
    [string]$Action = 'Install',

    [switch]$AcceptCloudflareTerms,

    [ValidateRange(1, 16)]
    [int]$Sample = 3,

    [ValidateRange(576, 1420)]
    [int]$Mtu = 1280,

    [string]$Sni = 'ozon.ru',
    [string]$RequiredCountries = '',
    [string]$RequiredNodes = '',
    [string]$ExcludedCountries = '',
    [string]$ExcludedNodes = '',
    [string]$DnsServers = '8.8.8.8, 8.8.4.4',

    [string]$BundledHelperHash = '',
    [switch]$ResumePreviousWarp
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)

$OfficialVersion = '3.1.0'
$StateDirectory = Join-Path $env:ProgramData 'WARP'
$HelperPath = Join-Path $StateDirectory 'warp-helper.exe'
$AccountPath = Join-Path $StateDirectory 'account.json'
$ConfigPath = Join-Path $StateDirectory 'warp.conf'
$CandidatePath = Join-Path $StateDirectory 'warp.candidate.conf'
$AmneziaPath = Join-Path $env:ProgramFiles 'AmneziaWG\amneziawg.exe'
$TunnelName = 'warp'
$ServiceName = 'AmneziaWGTunnel$warp'
$DnsServiceName = 'WarpExternalDNS'
$DnsConfigPath = Join-Path $StateDirectory 'dns.json'
. (Join-Path $PSScriptRoot 'dns_only.ps1')

function Test-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Assert-Administrator {
    if (-not (Test-Administrator)) {
        throw 'Запустите PowerShell от имени администратора.'
    }
}

function Get-ClientArchitecture {
    $architecture = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
    switch ($architecture) {
        'X64'   { return 'amd64' }
        'Arm64' { return 'arm64' }
        default { throw "Архитектура Windows $architecture не поддерживается." }
    }
}

function Get-ExpectedHashes([string]$Architecture) {
    if ($Architecture -eq 'amd64') {
        return @{
            Msi = 'a1b48ea8699cd347832a3691d832004574ef8ad65bcf887611ac8acb99b7de8b'
        }
    }
    return @{
        Msi = '3aec023884944890fac151f9cd4be3e92d39f9987c49da248837529c006ea2aa'
    }
}

function Get-VerifiedFile([string]$Uri, [string]$Destination, [string]$ExpectedHash) {
    Invoke-WebRequest -UseBasicParsing -Uri $Uri -OutFile $Destination
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $Destination).Hash.ToLowerInvariant()
    if ($actual -ne $ExpectedHash.ToLowerInvariant()) {
        Remove-Item -LiteralPath $Destination -Force -ErrorAction SilentlyContinue
        throw "Не совпала SHA-256 для $Uri"
    }
}

function Protect-StateDirectory {
    New-Item -ItemType Directory -Path $StateDirectory -Force | Out-Null
    & icacls.exe $StateDirectory /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw 'Не удалось ограничить права каталога WARP.'
    }
}

function Protect-StateFile([string]$Path) {
    & icacls.exe $Path /inheritance:r /grant:r '*S-1-5-18:F' '*S-1-5-32-544:F' | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Не удалось ограничить права файла $Path"
    }
}

function Install-AmneziaWG([string]$Architecture, [hashtable]$Hashes) {
    if (Test-Path -LiteralPath $AmneziaPath) {
        return
    }

    $msiName = "amneziawg-$Architecture-$OfficialVersion.msi"
    $msiPath = Join-Path $env:TEMP $msiName
    $uri = "https://github.com/amnezia-vpn/amneziawg-windows-client/releases/download/$OfficialVersion/$msiName"
    try {
        Write-Host "Скачивание официального AmneziaWG $OfficialVersion..."
        Get-VerifiedFile $uri $msiPath $Hashes.Msi
        $signature = Get-AuthenticodeSignature -LiteralPath $msiPath
        if ($signature.Status -ne [System.Management.Automation.SignatureStatus]::Valid) {
            throw "Некорректная Authenticode-подпись AmneziaWG: $($signature.Status)"
        }
        $process = Start-Process msiexec.exe -ArgumentList @('/i', "`"$msiPath`"", '/qn', 'DO_NOT_LAUNCH=1', '/norestart') -Wait -PassThru
        if ($process.ExitCode -notin @(0, 3010)) {
            throw "Установщик AmneziaWG завершился с кодом $($process.ExitCode)."
        }
    }
    finally {
        Remove-Item -LiteralPath $msiPath -Force -ErrorAction SilentlyContinue
    }
    if (-not (Test-Path -LiteralPath $AmneziaPath)) {
        throw 'После установки не найден amneziawg.exe.'
    }
}

function Install-Helper {
    if ($BundledHelperHash -notmatch '^[a-fA-F0-9]{64}$' -or -not (Test-Path -LiteralPath $HelperPath)) {
        throw 'Встроенный компонент регистрации не подготовлен. Запустите установку из warp-windows.exe.'
    }
    $currentHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $HelperPath).Hash.ToLowerInvariant()
    if ($currentHash -ne $BundledHelperHash.ToLowerInvariant()) {
        throw 'Не совпала SHA-256 встроенного компонента регистрации WARP.'
    }
    Protect-StateFile $HelperPath
}

function Invoke-Helper([string[]]$Arguments) {
    & $HelperPath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "Компонент регистрации и поиска WARP завершился с кодом $LASTEXITCODE."
    }
}

function Ensure-Account {
    if (Test-Path -LiteralPath $AccountPath) {
        return
    }
    if (-not $AcceptCloudflareTerms) {
        throw 'Для регистрации прочитайте условия Cloudflare и повторите с -AcceptCloudflareTerms.'
    }
    Write-Host 'Регистрация отдельного WARP-профиля для этого компьютера...'
    Invoke-Helper @(
        'register', '--plain', '--relay', 'none', '--account', $AccountPath,
        '--registration-sni', $Sni
    )
    Protect-StateFile $AccountPath
}

function Add-OptionalArgument([Collections.Generic.List[string]]$Arguments, [string]$Name, [string]$Value) {
    if (-not [string]::IsNullOrWhiteSpace($Value)) {
        $Arguments.Add($Name)
        $Arguments.Add($Value.Trim())
    }
}

function Get-ValidatedDNS([string]$Value) {
    $addresses = [Collections.Generic.List[string]]::new()
    foreach ($part in $Value.Split(',')) {
        $text = $part.Trim()
        $address = $null
        if ([string]::IsNullOrWhiteSpace($text) -or -not [Net.IPAddress]::TryParse($text, [ref]$address)) {
            throw 'Укажите IP-адреса DNS-серверов через запятую.'
        }
        # TryParse accepts abbreviated decimal IPv4; require its unambiguous dotted form.
        if ($address.AddressFamily -eq [Net.Sockets.AddressFamily]::InterNetwork -and $text -ne $address.ToString()) {
            throw 'IPv4 DNS должен иметь вид 8.8.8.8.'
        }
        $addresses.Add($address.ToString())
    }
    return $addresses -join ', '
}

function Set-ConfigurationDNS([string]$Config, [string]$Value) {
    $dns = Get-ValidatedDNS $Value
    $lines = [Collections.Generic.List[string]]::new()
    $inInterface = $false
    $interfaces = 0
    foreach ($line in [regex]::Split($Config, '\r?\n')) {
        if ($line -match '^\s*\[([^\]]+)\]\s*$') {
            $inInterface = $Matches[1] -eq 'Interface'
            if ($inInterface) { $interfaces++ }
            $lines.Add($line)
            if ($inInterface) { $lines.Add("DNS = $dns") }
        }
        elseif (-not ($inInterface -and $line -match '^\s*DNS\s*=')) {
            $lines.Add($line)
        }
    }
    if ($interfaces -ne 1) { throw 'В конфигурации WARP должна быть одна секция [Interface].' }
    $separator = "`n"
    if ($Config.Contains("`r`n")) { $separator = "`r`n" }
    return $lines -join $separator
}

function Stop-ExternalDNS {
    $dnsService = Get-Service -Name $DnsServiceName -ErrorAction SilentlyContinue
    if ($null -ne $dnsService) {
        try {
            if ($dnsService.Status -ne [ServiceProcess.ServiceControllerStatus]::Stopped) {
                Stop-Service -Name $DnsServiceName
                $dnsService.WaitForStatus([ServiceProcess.ServiceControllerStatus]::Stopped, [TimeSpan]::FromSeconds(30))
            }
        }
        finally { $dnsService.Dispose() }
    }
}

function Start-ExternalDNS {
    if (-not (Test-Path -LiteralPath $DnsConfigPath) -or -not (Test-Path -LiteralPath $ConfigPath)) { return }
    $dns = Get-Content -LiteralPath $DnsConfigPath -Raw | ConvertFrom-Json
    if (-not $dns.enabled -or $dns.type -notin @('dot', 'doh')) { return }
    $config = Get-Content -LiteralPath $ConfigPath -Raw
    if ($config -notmatch '(?im)^\s*DNS\s*=\s*127\.0\.0\.1\s*$') { return }
    $dnsService = Get-Service -Name $DnsServiceName -ErrorAction SilentlyContinue
    if ($null -eq $dnsService) { throw 'Не установлена служба стороннего DNS. Примените DNS в окне WARP.' }
    try {
        Start-Service -Name $DnsServiceName
        $dnsService.WaitForStatus([ServiceProcess.ServiceControllerStatus]::Running, [TimeSpan]::FromSeconds(30))
    }
    finally { $dnsService.Dispose() }
}

function Remove-ExternalDNS {
    Stop-ExternalDNS
    $dnsService = Get-Service -Name $DnsServiceName -ErrorAction SilentlyContinue
    if ($null -eq $dnsService) { return }
    $dnsService.Dispose()
    & sc.exe delete $DnsServiceName | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Не удалось удалить службу стороннего DNS.' }
    for ($attempt = 0; $attempt -lt 40; $attempt++) {
        $remaining = Get-Service -Name $DnsServiceName -ErrorAction SilentlyContinue
        if ($null -eq $remaining) { return }
        $remaining.Dispose()
        Start-Sleep -Milliseconds 250
    }
    throw 'Служба стороннего DNS ещё удаляется. Закройте оснастку служб и повторите удаление.'
}

function Set-TunnelDNS {
    if (-not (Test-Path -LiteralPath $ConfigPath)) { throw 'WARP не установлен. Сначала выполните Install.' }
    $config = [IO.File]::ReadAllText($ConfigPath)
    $updated = Set-ConfigurationDNS $config $DnsServers
    $service = Get-TunnelService
    if ($null -eq $service) { throw 'Служба WARP не установлена. Сначала выполните Install.' }
    $wasRunning = $service.Status -eq [ServiceProcess.ServiceControllerStatus]::Running
    $service.Dispose()
    $dnsService = Get-Service -Name $DnsServiceName -ErrorAction SilentlyContinue
    $dnsWasRunning = $false
    if ($null -ne $dnsService) {
        $dnsWasRunning = $dnsService.Status -eq [ServiceProcess.ServiceControllerStatus]::Running
        $dnsService.Dispose()
    }
    $operation = [Guid]::NewGuid().ToString('N')
    $candidate = Join-Path $StateDirectory "dns-$operation.candidate"
    $backup = Join-Path $StateDirectory "dns-$operation.backup"
    $replaced = $false
    $rollbackFailed = $false
    try {
        [IO.File]::WriteAllText($candidate, $updated, [Text.UTF8Encoding]::new($false))
        Protect-StateFile $candidate
        Stop-ExternalDNS
        Stop-Service -Name $ServiceName
        (Get-Service -Name $ServiceName).WaitForStatus([ServiceProcess.ServiceControllerStatus]::Stopped, [TimeSpan]::FromSeconds(30))
        [IO.File]::Replace($candidate, $ConfigPath, $backup)
        $replaced = $true
        Protect-StateFile $ConfigPath
        if ($wasRunning) {
            Start-Service -Name $ServiceName
            (Get-Service -Name $ServiceName).WaitForStatus([ServiceProcess.ServiceControllerStatus]::Running, [TimeSpan]::FromSeconds(30))
        }
        # The GUI configures/starts the selected DNS service after this transaction.
        Write-Host 'DNS конфигурации WARP обновлён без поиска endpoint.'
    }
    catch {
        $originalError = $_
        try {
            if ($replaced) {
                Stop-ExternalDNS
                Stop-Service -Name $ServiceName
                (Get-Service -Name $ServiceName).WaitForStatus([ServiceProcess.ServiceControllerStatus]::Stopped, [TimeSpan]::FromSeconds(30))
                [IO.File]::Replace($backup, $ConfigPath, $candidate)
            }
            if ($wasRunning) {
                Start-Service -Name $ServiceName
                (Get-Service -Name $ServiceName).WaitForStatus([ServiceProcess.ServiceControllerStatus]::Running, [TimeSpan]::FromSeconds(30))
            }
            if ($dnsWasRunning) {
                Start-Service -Name $DnsServiceName
                (Get-Service -Name $DnsServiceName).WaitForStatus([ServiceProcess.ServiceControllerStatus]::Running, [TimeSpan]::FromSeconds(30))
            }
        }
        catch {
            $rollbackFailed = $true
            $recoveryPath = $ConfigPath
            if (Test-Path -LiteralPath $backup) { $recoveryPath = $backup }
            throw "Не удалось применить DNS: $originalError. Ошибка восстановления: $_. Конфигурация для восстановления: $recoveryPath"
        }
        throw $originalError
    }
    finally {
        Remove-Item -LiteralPath $candidate -Force -ErrorAction SilentlyContinue
        # Keep the backup when rollback itself failed so the user can recover it.
        if (-not $rollbackFailed) {
            Remove-Item -LiteralPath $backup -Force -ErrorAction SilentlyContinue
        }
    }
}

function New-WarpConfiguration {
    Remove-Item -LiteralPath $CandidatePath -Force -ErrorAction SilentlyContinue
    $arguments = [Collections.Generic.List[string]]::new()
    @(
        'scan', '--plain', '--account', $AccountPath, '--proto', 'awg',
        '--gen-i1', 'quic', '--i1-sni', $Sni,
        '--sample', [string]$Sample, '--tunnel-jobs', '1', '--timeout', '4', '--auto-mask',
        '--tun-ping-count', '10', '--ping-target', '8.8.8.8',
        '--no-report', '--conf', $CandidatePath, '--mtu', [string]$Mtu
    ) | ForEach-Object { $arguments.Add($_) }
    Add-OptionalArgument $arguments '--country' $RequiredCountries
    Add-OptionalArgument $arguments '--node' $RequiredNodes
    Add-OptionalArgument $arguments '--exclude-country' $ExcludedCountries
    Add-OptionalArgument $arguments '--exclude-node' $ExcludedNodes

    Write-Host 'Поиск минимальной потери пакетов и лучшей задержки...'
    Invoke-Helper $arguments.ToArray()
    if (-not (Test-Path -LiteralPath $CandidatePath)) {
        throw 'Компонент поиска не создал конфигурацию WARP.'
    }

    $config = Get-Content -LiteralPath $CandidatePath -Raw
    if ($config -notmatch '(?m)^Address\s*=\s*' -or $config -notmatch '(?m)^AllowedIPs\s*=\s*0\.0\.0\.0/0,\s*::/0\s*$') {
        throw 'Созданная конфигурация не является full-tunnel.'
    }
    $config = Set-ConfigurationDNS $config $DnsServers
    [IO.File]::WriteAllText($CandidatePath, $config, [Text.UTF8Encoding]::new($false))
    Protect-StateFile $CandidatePath
}

function Update-WarpTunnel {
    $service = Get-TunnelService
    $wasRunning = $false
    if ($null -ne $service) {
        try { $wasRunning = $service.Status -eq [ServiceProcess.ServiceControllerStatus]::Running }
        finally { $service.Dispose() }
    }
    try {
        # Probing through our own full tunnel can create a nested UDP path.
        # Stop only WARP; leave unrelated VPN services and their routes alone.
        if ($wasRunning) {
            Stop-ExternalDNS
            Stop-Service -Name $ServiceName
            (Get-Service -Name $ServiceName).WaitForStatus([ServiceProcess.ServiceControllerStatus]::Stopped, [TimeSpan]::FromSeconds(30))
        }
        Ensure-Account
        New-WarpConfiguration
        Install-TunnelConfiguration
    }
    catch {
        $originalError = $_
        if ($wasRunning) {
            try {
                Start-Service -Name $ServiceName
                (Get-Service -Name $ServiceName).WaitForStatus([ServiceProcess.ServiceControllerStatus]::Running, [TimeSpan]::FromSeconds(30))
                Start-ExternalDNS
            }
            catch { throw "$originalError`nОшибка восстановления прежнего WARP: $_" }
        }
        throw $originalError
    }
}

function Remove-TunnelService {
    Stop-ExternalDNS
    $service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($null -eq $service) {
        return
    }
    $service.Dispose()
    & $AmneziaPath /uninstalltunnelservice $TunnelName | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw 'Не удалось удалить прежнюю службу туннеля.'
    }
    for ($attempt = 0; $attempt -lt 20; $attempt++) {
        if ($null -eq (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue)) {
            return
        }
        Start-Sleep -Milliseconds 250
    }
    throw 'Прежняя служба туннеля не остановилась вовремя.'
}

function Install-TunnelService {
    & $AmneziaPath /installtunnelservice $ConfigPath | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw 'Не удалось установить службу WARP.'
    }
    $service = Get-Service -Name $ServiceName -ErrorAction Stop
    try {
        $service.WaitForStatus([ServiceProcess.ServiceControllerStatus]::Running, [TimeSpan]::FromSeconds(30))
    }
    finally { $service.Dispose() }
}

function Install-TunnelConfiguration {
    if (-not (Test-Path -LiteralPath $CandidatePath)) { throw 'Не найдена новая конфигурация WARP.' }
    $hadConfig = Test-Path -LiteralPath $ConfigPath
    $service = Get-TunnelService
    $hadService = $null -ne $service
    $wasRunning = $false
    if ($hadService) {
        $wasRunning = $service.Status -eq [ServiceProcess.ServiceControllerStatus]::Running
        $service.Dispose()
        if (-not $hadConfig) { throw 'Отсутствует конфигурация установленной службы WARP; восстановите warp.conf перед переустановкой.' }
    }
    $dnsService = Get-Service -Name $DnsServiceName -ErrorAction SilentlyContinue
    $dnsWasRunning = $false
    if ($null -ne $dnsService) {
        $dnsWasRunning = $dnsService.Status -eq [ServiceProcess.ServiceControllerStatus]::Running
        $dnsService.Dispose()
    }
    $backup = Join-Path $StateDirectory ('install-' + [Guid]::NewGuid().ToString('N') + '.backup')
    $servicesChanged = $false
    $configChanged = $false
    $rollbackFailed = $false
    try {
        if ($hadConfig) {
            [IO.File]::Copy($ConfigPath, $backup, $false)
            Protect-StateFile $backup
        }
        Protect-StateFile $CandidatePath
        $servicesChanged = $true
        Remove-TunnelService
        $configChanged = $true
        if ($hadConfig) {
            [IO.File]::Replace($CandidatePath, $ConfigPath, $backup)
        }
        else {
            [IO.File]::Move($CandidatePath, $ConfigPath)
        }
        Protect-StateFile $ConfigPath
        Install-TunnelService
        # Go starts the matching DNS proxy only after configuration succeeds.
    }
    catch {
        $originalError = $_
        try {
            if ($servicesChanged) {
                Remove-TunnelService
                if ($configChanged) {
                    if ($hadConfig) {
                        [IO.File]::Copy($backup, $ConfigPath, $true)
                        Protect-StateFile $ConfigPath
                    }
                    elseif (Test-Path -LiteralPath $ConfigPath) {
                        Remove-Item -LiteralPath $ConfigPath -Force
                    }
                }
                if ($hadService) {
                    Install-TunnelService
                    if (-not $wasRunning) {
                        Stop-Service -Name $ServiceName
                        (Get-Service -Name $ServiceName).WaitForStatus([ServiceProcess.ServiceControllerStatus]::Stopped, [TimeSpan]::FromSeconds(30))
                    }
                }
                if ($dnsWasRunning) {
                    Start-Service -Name $DnsServiceName
                    (Get-Service -Name $DnsServiceName).WaitForStatus([ServiceProcess.ServiceControllerStatus]::Running, [TimeSpan]::FromSeconds(30))
                }
            }
        }
        catch {
            $rollbackFailed = $true
            $recovery = 'Неполная установка; проверьте службу WARP.'
            if ($hadConfig) { $recovery = "Сохранена прежняя конфигурация: $backup" }
            throw "Не удалось установить туннель: $originalError. Ошибка восстановления: $_. $recovery"
        }
        throw $originalError
    }
    finally {
        Remove-Item -LiteralPath $CandidatePath -Force -ErrorAction SilentlyContinue
        if (-not $rollbackFailed) {
            Remove-Item -LiteralPath $backup -Force -ErrorAction SilentlyContinue
        }
    }
}

function Get-TunnelService {
    return Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
}

function Get-AmneziaProducts {
    # Reading uninstall entries does not trigger MSI repair (Win32_Product does).
    foreach ($base in @('HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall', 'HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall')) {
        if (-not (Test-Path -LiteralPath $base)) { continue }
        foreach ($key in Get-ChildItem -LiteralPath $base) {
            $product = Get-ItemProperty -LiteralPath $key.PSPath -ErrorAction SilentlyContinue
            if ($null -eq $product -or -not $product.PSObject.Properties['DisplayName'] -or $product.DisplayName -ne 'AmneziaWG') { continue }
            if (-not $product.PSObject.Properties['Publisher'] -or $product.Publisher -ne 'Amnezia') { continue }
            if ($key.PSChildName -notmatch '^\{[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}\}$') { continue }
            $key.PSChildName
        }
    }
}

function Remove-AmneziaProduct([string]$ProductCode) {
    if ($ProductCode -notmatch '^\{[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}\}$') { throw 'Некорректный идентификатор MSI AmneziaWG.' }
    $process = Start-Process msiexec.exe -ArgumentList @('/x', $ProductCode, '/qn', '/norestart') -Wait -PassThru
    if ($process.ExitCode -eq 3010) { $script:UninstallNeedsReboot = $true }
    elseif ($process.ExitCode -notin @(0, 1605, 1614)) { throw "Удаление AmneziaWG завершилось с кодом $($process.ExitCode). Повторите удаление после завершения других MSI-операций." }
}

function Remove-AmneziaService([string]$Name) {
    if ($Name -ne 'AmneziaWGManager' -and $Name -notmatch '^AmneziaWGTunnel\$[^\\/]+$') { throw 'Недопустимая служба для удаления.' }
    $service = Get-Service -Name $Name -ErrorAction SilentlyContinue
    if ($null -eq $service) { return }
    try {
        if ($service.Status -ne [ServiceProcess.ServiceControllerStatus]::Stopped) {
            Stop-Service -Name $Name -Force
            $service.WaitForStatus([ServiceProcess.ServiceControllerStatus]::Stopped, [TimeSpan]::FromSeconds(30))
        }
    }
    finally { $service.Dispose() }
    & sc.exe delete $Name | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Не удалось удалить службу $Name" }
}

function Remove-AmneziaWG {
    $script:UninstallNeedsReboot = $false
    $products = @(Get-AmneziaProducts | Select-Object -Unique)
    foreach ($code in $products) {
        Write-Host 'Удаление официального AmneziaWG и его драйвера...'
        Remove-AmneziaProduct $code
    }
    # Also handle a portable/partially installed backend, without touching
    # ordinary WireGuard services or shared Windows driver stores.
    $services = @(Get-Service -Name 'AmneziaWGManager', 'AmneziaWGTunnel$*' -ErrorAction SilentlyContinue)
    foreach ($service in $services) {
        $name = $service.Name
        $service.Dispose()
        Remove-AmneziaService $name
    }
    $backendDirectory = [IO.Path]::GetDirectoryName($AmneziaPath)
    foreach ($process in @(Get-Process -Name 'amneziawg', 'awg' -ErrorAction SilentlyContinue)) {
        if ($process.Path -and [string]::Equals([IO.Path]::GetDirectoryName($process.Path), $backendDirectory, [StringComparison]::OrdinalIgnoreCase)) {
            Stop-Process -Id $process.Id -Force
        }
    }
    if ($products.Count -eq 0 -and (Test-Path -LiteralPath $AmneziaPath)) {
        & $AmneziaPath /removedriver | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Не удалось удалить драйвер AmneziaWG.' }
    }
}

function Get-WarpCleanupRoots {
    $roots = @($StateDirectory, [IO.Path]::GetDirectoryName($AmneziaPath))
    foreach ($base in @($env:LOCALAPPDATA, $env:APPDATA)) {
        if ([string]::IsNullOrWhiteSpace($base)) { continue }
        $roots += (Join-Path $base 'WARP')
        $roots += (Join-Path $base 'AmneziaWG')
    }
    foreach ($root in $roots) {
        if ([string]::IsNullOrWhiteSpace($root)) { continue }
        [IO.Path]::GetFullPath($root).TrimEnd([IO.Path]::DirectorySeparatorChar)
    }
}

function Get-WarpTemporaryDirectory { return [IO.Path]::GetTempPath() }

function Remove-WarpTreeEntry([string]$Path) {
    try { $item = Get-Item -LiteralPath $Path -Force -ErrorAction Stop }
    catch [System.Management.Automation.ItemNotFoundException] { return }
    # Never recurse through a junction/symlink into unrelated user data.
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        if ($item.PSIsContainer) { [IO.Directory]::Delete($item.FullName) }
        else { [IO.File]::Delete($item.FullName) }
        return
    }
    if ($item.PSIsContainer) {
        foreach ($child in Get-ChildItem -LiteralPath $item.FullName -Force) { Remove-WarpTreeEntry $child.FullName }
    }
    Remove-Item -LiteralPath $item.FullName -Force
}

function Remove-WarpOwnedTree([string]$Path) {
    $full = [IO.Path]::GetFullPath($Path).TrimEnd([IO.Path]::DirectorySeparatorChar)
    $allowed = $false
    foreach ($root in Get-WarpCleanupRoots) {
        if ([string]::Equals($full, $root, [StringComparison]::OrdinalIgnoreCase)) { $allowed = $true }
    }
    $temporary = (Get-WarpTemporaryDirectory).TrimEnd([IO.Path]::DirectorySeparatorChar)
    if ([string]::Equals([IO.Path]::GetDirectoryName($full), $temporary, [StringComparison]::OrdinalIgnoreCase) -and [IO.Path]::GetFileName($full) -match '^warp-windows-[0-9]+$') { $allowed = $true }
    if (-not $allowed) { throw "Отказ от очистки постороннего каталога: $full" }
    for ($attempt = 0; $attempt -lt 20; $attempt++) {
        try { Remove-WarpTreeEntry $full; return }
        catch {
            if ($attempt -eq 19) { throw }
            Start-Sleep -Milliseconds 200
        }
    }
}

function Remove-WarpCaches {
    $temporary = Get-WarpTemporaryDirectory
    $activeScript = ''
    if (-not [string]::IsNullOrWhiteSpace($PSScriptRoot)) { $activeScript = [IO.Path]::GetFullPath($PSScriptRoot).TrimEnd([IO.Path]::DirectorySeparatorChar) }
    foreach ($entry in Get-ChildItem -LiteralPath $temporary -Force) {
        if ($entry.PSIsContainer -and $entry.Name -match '^warp-windows-[0-9]+$') {
            if (-not [string]::Equals($entry.FullName, $activeScript, [StringComparison]::OrdinalIgnoreCase)) { Remove-WarpOwnedTree $entry.FullName }
        }
        elseif (-not $entry.PSIsContainer -and $entry.Name -match '^amneziawg-(amd64|arm64)-[0-9]+(\.[0-9]+){1,3}\.msi$') {
            Remove-Item -LiteralPath $entry.FullName -Force
        }
    }
}

function Remove-WarpCompletely {
    Restore-DNSOnly
    Remove-ExternalDNS
    if (Test-Path -LiteralPath $AmneziaPath) { Remove-TunnelService }
    Remove-AmneziaWG
    foreach ($root in Get-WarpCleanupRoots | Select-Object -Unique) { Remove-WarpOwnedTree $root }
    foreach ($key in @('HKLM:\SOFTWARE\AmneziaWG', 'HKLM:\SOFTWARE\WOW6432Node\AmneziaWG', 'HKCU:\SOFTWARE\AmneziaWG')) {
        if (Test-Path -LiteralPath $key) { Remove-Item -LiteralPath $key -Recurse -Force }
    }
    Remove-WarpCaches
    Write-Host 'WARP, AmneziaWG, их службы, профили, ключи, настройки и кэши удалены.'
    if ($script:UninstallNeedsReboot) { Write-Host 'Windows требует перезагрузку для завершения удаления драйвера. Автоматическая перезагрузка не выполняется.' }
}

$architecture = Get-ClientArchitecture
$hashes = Get-ExpectedHashes $architecture

if ($Action -ne 'Status') {
    Assert-Administrator
}

switch ($Action) {
    'PrepareDNSOnly' { Prepare-DNSOnly }
    'ApplyDNSOnly' { Enable-DNSOnly }
    'StopDNSOnly' { Restore-DNSOnly -ResumeWarp:$ResumePreviousWarp }
    'Install' {
        Protect-StateDirectory
        Install-AmneziaWG $architecture $hashes
        Install-Helper
        Update-WarpTunnel
        Write-Host 'WARP подключён. IPv4, IPv6 и DNS по умолчанию направлены через туннель.'
    }
    'Rescan' {
        Protect-StateDirectory
        Install-AmneziaWG $architecture $hashes
        Install-Helper
        Update-WarpTunnel
        Write-Host 'Выбран лучший из проверенных endpoint, WARP переподключён.'
    }
    'Connect' {
        $service = Get-TunnelService
        if ($null -eq $service) { throw 'Служба WARP не установлена. Сначала выполните Install.' }
        Start-Service -Name $ServiceName
        (Get-Service -Name $ServiceName).WaitForStatus([ServiceProcess.ServiceControllerStatus]::Running, [TimeSpan]::FromSeconds(30))
        Start-ExternalDNS
        Write-Host 'WARP подключён.'
    }
    'Disconnect' {
        Stop-ExternalDNS
        $service = Get-TunnelService
        if ($null -ne $service) {
            Stop-Service -Name $ServiceName -Force
            (Get-Service -Name $ServiceName).WaitForStatus([ServiceProcess.ServiceControllerStatus]::Stopped, [TimeSpan]::FromSeconds(30))
        }
        Write-Host 'WARP отключён.'
    }
    'Reconnect' {
        $service = Get-TunnelService
        if ($null -eq $service) { throw 'Служба WARP не установлена. Сначала выполните Install.' }
        Stop-ExternalDNS
        Restart-Service -Name $ServiceName
        (Get-Service -Name $ServiceName).WaitForStatus([ServiceProcess.ServiceControllerStatus]::Running, [TimeSpan]::FromSeconds(30))
        Start-ExternalDNS
        Write-Host 'WARP переподключён к текущему endpoint.'
    }
    'ApplyDNS' {
        Set-TunnelDNS
    }
    'Status' {
        if (Test-Path -LiteralPath (Get-DNSOnlySnapshotPath)) {
            Write-Host 'Режим «Только DNS»: включён. WARP отключён.'
            if (Test-Path -LiteralPath $DnsConfigPath) {
                $dns = Get-Content -LiteralPath $DnsConfigPath -Raw | ConvertFrom-Json
                Write-Host "DNS: $($dns.type) $($dns.address)"
            }
            Get-Service -Name $DnsServiceName -ErrorAction SilentlyContinue | Select-Object Name, Status, StartType
            break
        }
        $service = Get-TunnelService
        if ($null -eq $service) {
            Write-Host 'WARP не установлен.'
        }
        else {
            $service | Select-Object Name, Status, StartType
            Write-Host "Full-tunnel config: $ConfigPath"
            if (Test-Path -LiteralPath $DnsConfigPath) {
                $dns = Get-Content -LiteralPath $DnsConfigPath -Raw | ConvertFrom-Json
                if ($dns.enabled) {
                    Write-Host "Сторонний DNS: $($dns.type) $($dns.address)"
                    Get-Service -Name $DnsServiceName -ErrorAction SilentlyContinue | Select-Object Name, Status, StartType
                }
            }
        }
    }
    'Uninstall' {
        Remove-WarpCompletely
    }
}

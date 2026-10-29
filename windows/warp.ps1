[CmdletBinding()]
param(
    [ValidateSet('Install', 'Connect', 'Disconnect', 'Reconnect', 'Rescan', 'Status', 'Uninstall')]
    [string]$Action = 'Install',

    [switch]$AcceptCloudflareTerms,

    [ValidateRange(1, 16)]
    [int]$Sample = 3,

    [ValidateRange(576, 1420)]
    [int]$Mtu = 1280,

    [string]$Sni = 'ozon.ru',
    [string]$RequiredCountries = '',
    [string]$RequiredNodes = '',
    [string]$ExcludedCountries = 'RU',
    [string]$ExcludedNodes = 'DME',
    [string]$DnsServers = '8.8.8.8, 8.8.4.4'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$ReleaseTag = 'v4.1.0'
$HelperVersion = '0.16.0-r2'
$Repository = 'BAzeRlok/CFWARP-OPENWRT'
$OfficialVersion = '3.1.0'
$StateDirectory = Join-Path $env:ProgramData 'WARP'
$HelperPath = Join-Path $StateDirectory 'warp-helper.exe'
$AccountPath = Join-Path $StateDirectory 'account.json'
$ConfigPath = Join-Path $StateDirectory 'warp.conf'
$CandidatePath = Join-Path $StateDirectory 'warp.candidate.conf'
$AmneziaPath = Join-Path $env:ProgramFiles 'AmneziaWG\amneziawg.exe'
$TunnelName = 'warp'
$ServiceName = 'AmneziaWGTunnel$warp'

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
            Helper = 'd0007949f2f9d36c711ad6be075ea71cf5c19a6c48d14a8f57501617542ae506'
            Msi = 'a1b48ea8699cd347832a3691d832004574ef8ad65bcf887611ac8acb99b7de8b'
        }
    }
    return @{
        Helper = '5656a034d0b7352ef5a8148cb65e29fe8f964b03eae2f91f18dcf933fb439a1c'
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

function Install-Helper([string]$Architecture, [hashtable]$Hashes) {
    $name = "warp-windows-helper-$HelperVersion-$Architecture.exe"
    $uri = "https://github.com/$Repository/releases/download/$ReleaseTag/$name"
    $needsDownload = $true
    if (Test-Path -LiteralPath $HelperPath) {
        $currentHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $HelperPath).Hash.ToLowerInvariant()
        $needsDownload = $currentHash -ne $Hashes.Helper.ToLowerInvariant()
    }
    if ($needsDownload) {
        $temporary = "$HelperPath.download"
        Write-Host 'Скачивание проверенного WARP helper...'
        Get-VerifiedFile $uri $temporary $Hashes.Helper
        Move-Item -LiteralPath $temporary -Destination $HelperPath -Force
    }
    Protect-StateFile $HelperPath
}

function Invoke-Helper([string[]]$Arguments) {
    & $HelperPath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "WARP helper завершился с кодом $LASTEXITCODE."
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
        '--gen-i1', 'quic', '--i1-sni', $Sni
    )
    Protect-StateFile $AccountPath
}

function Add-OptionalArgument([Collections.Generic.List[string]]$Arguments, [string]$Name, [string]$Value) {
    if (-not [string]::IsNullOrWhiteSpace($Value)) {
        $Arguments.Add($Name)
        $Arguments.Add($Value.Trim())
    }
}

function New-WarpConfiguration {
    Remove-Item -LiteralPath $CandidatePath -Force -ErrorAction SilentlyContinue
    $arguments = [Collections.Generic.List[string]]::new()
    @(
        'scan', '--plain', '--account', $AccountPath, '--proto', 'awg',
        '--gen-i1', 'quic', '--i1-sni', $Sni,
        '--sample', [string]$Sample, '--tunnel-jobs', '2',
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
        throw 'Helper не создал конфигурацию WARP.'
    }

    $config = Get-Content -LiteralPath $CandidatePath -Raw
    if ($config -notmatch '(?m)^Address\s*=\s*' -or $config -notmatch '(?m)^AllowedIPs\s*=\s*0\.0\.0\.0/0,\s*::/0\s*$') {
        throw 'Созданная конфигурация не является full-tunnel.'
    }
    $escapedDns = $DnsServers.Trim()
    if ($escapedDns -notmatch '^[0-9A-Fa-f:., ]+$') {
        throw 'Некорректный список DNS-серверов.'
    }
    $config = [regex]::Replace($config, '(?m)^DNS\s*=.*$', "DNS = $escapedDns")
    [IO.File]::WriteAllText($CandidatePath, $config, [Text.UTF8Encoding]::new($false))
    Protect-StateFile $CandidatePath
}

function Remove-TunnelService {
    $service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($null -eq $service) {
        return
    }
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

function Install-TunnelConfiguration {
    Remove-TunnelService
    Move-Item -LiteralPath $CandidatePath -Destination $ConfigPath -Force
    Protect-StateFile $ConfigPath
    & $AmneziaPath /installtunnelservice $ConfigPath | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw 'Не удалось установить службу WARP.'
    }
    $service = Get-Service -Name $ServiceName -ErrorAction Stop
    $service.WaitForStatus([ServiceProcess.ServiceControllerStatus]::Running, [TimeSpan]::FromSeconds(30))
}

function Get-TunnelService {
    return Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
}

$architecture = Get-ClientArchitecture
$hashes = Get-ExpectedHashes $architecture

if ($Action -ne 'Status') {
    Assert-Administrator
}

switch ($Action) {
    'Install' {
        Protect-StateDirectory
        Install-AmneziaWG $architecture $hashes
        Install-Helper $architecture $hashes
        Ensure-Account
        New-WarpConfiguration
        Install-TunnelConfiguration
        Write-Host 'WARP подключён. IPv4, IPv6 и DNS по умолчанию направлены через туннель.'
    }
    'Rescan' {
        Protect-StateDirectory
        Install-AmneziaWG $architecture $hashes
        Install-Helper $architecture $hashes
        Ensure-Account
        New-WarpConfiguration
        Install-TunnelConfiguration
        Write-Host 'Выбран лучший из проверенных endpoint, WARP переподключён.'
    }
    'Connect' {
        $service = Get-TunnelService
        if ($null -eq $service) { throw 'Служба WARP не установлена. Сначала выполните Install.' }
        Start-Service -Name $ServiceName
        (Get-Service -Name $ServiceName).WaitForStatus([ServiceProcess.ServiceControllerStatus]::Running, [TimeSpan]::FromSeconds(30))
        Write-Host 'WARP подключён.'
    }
    'Disconnect' {
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
        Restart-Service -Name $ServiceName -Force
        (Get-Service -Name $ServiceName).WaitForStatus([ServiceProcess.ServiceControllerStatus]::Running, [TimeSpan]::FromSeconds(30))
        Write-Host 'WARP переподключён к текущему endpoint.'
    }
    'Status' {
        $service = Get-TunnelService
        if ($null -eq $service) {
            Write-Host 'WARP не установлен.'
        }
        else {
            $service | Select-Object Name, Status, StartType
            Write-Host "Full-tunnel config: $ConfigPath"
        }
    }
    'Uninstall' {
        if (Test-Path -LiteralPath $AmneziaPath) {
            Remove-TunnelService
        }
        if (Test-Path -LiteralPath $StateDirectory) {
            Remove-Item -LiteralPath $StateDirectory -Recurse -Force
        }
        Write-Host 'Профиль WARP и локальные ключи удалены. AmneziaWG оставлен установленным.'
    }
}

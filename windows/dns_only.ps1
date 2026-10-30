# Loaded by warp.ps1. Network settings are changed only by explicit actions.
function Get-DNSOnlySnapshotPath { return Join-Path $StateDirectory 'dns-only-system.json' }

function Write-DNSOnlySnapshot($Snapshot) {
    $path = Get-DNSOnlySnapshotPath
    $temporary = "$path.new"
    try {
        [IO.File]::WriteAllText($temporary, ($Snapshot | ConvertTo-Json -Depth 8), [Text.UTF8Encoding]::new($false))
        Protect-StateFile $temporary
        if (Test-Path -LiteralPath $path) { [IO.File]::Replace($temporary, $path, $null) }
        else { [IO.File]::Move($temporary, $path) }
    }
    finally { if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force } }
}

function Get-AdapterDNSOverride([string]$Guid, [string]$Family) {
    $protocol = 'Tcpip'
    if ($Family -eq 'IPv6') { $protocol = 'Tcpip6' }
    $path = "HKLM:\SYSTEM\CurrentControlSet\Services\$protocol\Parameters\Interfaces\$Guid"
    $value = Get-ItemProperty -LiteralPath $path -ErrorAction Stop
    $property = $value.PSObject.Properties['NameServer']
    if ($null -eq $property) { return @() }
    return @(([string]$property.Value -split '[,;\s]+') | Where-Object { $_ })
}

function Read-DNSOnlySnapshot {
    $path = Get-DNSOnlySnapshotPath
    if (-not (Test-Path -LiteralPath $path)) { return $null }
    $state = [IO.File]::ReadAllText($path) | ConvertFrom-Json
    if ($state.version -ne 1 -or @($state.interfaces).Count -eq 0) { throw 'Повреждена копия системных настроек DNS.' }
    foreach ($adapter in $state.interfaces) {
        $guid = [Guid]::Empty
        if (-not [Guid]::TryParse($adapter.guid, [ref]$guid)) { throw 'Повреждён идентификатор сетевого адаптера в копии DNS.' }
        foreach ($family in $adapter.families) {
            if ($family.name -notin @('IPv4', 'IPv6')) { throw 'Повреждена копия семейства IP в настройках DNS.' }
            if (-not $family.automatic -and @($family.servers).Count -eq 0) { throw 'В копии отсутствуют статические DNS-серверы.' }
            foreach ($server in $family.servers) {
                $ip = $null
                if (-not [Net.IPAddress]::TryParse($server, [ref]$ip)) { throw 'Повреждён IP DNS-сервера в копии настроек.' }
            }
        }
    }
    if ($state.warpStartType -notin @('', 'Automatic', 'Manual', 'Disabled')) { throw 'Повреждена копия режима запуска WARP.' }
    return $state
}

function Prepare-DNSOnly {
    Protect-StateDirectory
    $state = Read-DNSOnlySnapshot
    if ($null -eq $state) {
        # Preserve only connected adapters carrying a default route. Match by
        # GUID on restore: Windows can change an interface index after reboot.
        $routes = @(Get-NetRoute -ErrorAction Stop | Where-Object { $_.DestinationPrefix -in @('0.0.0.0/0', '::/0') })
        $indexes = @($routes | Select-Object -ExpandProperty InterfaceIndex -Unique)
        $interfaces = @()
        foreach ($adapter in @(Get-NetAdapter -IncludeHidden -ErrorAction Stop)) {
            if ($adapter.ifIndex -notin $indexes -or $adapter.Name -eq $TunnelName -or $adapter.Status -ne 'Up') { continue }
            $guid = ([Guid]$adapter.InterfaceGuid).ToString('B')
            $families = @()
            foreach ($family in @('IPv4', 'IPv6')) {
                $dns = @(Get-DnsClientServerAddress -InterfaceIndex $adapter.ifIndex -AddressFamily $family -ErrorAction Stop)
                if ($dns.Count -eq 0) { continue }
                $servers = @(Get-AdapterDNSOverride $guid $family)
                $families += [pscustomobject]@{ name = $family; automatic = ($servers.Count -eq 0); servers = $servers }
            }
            if ($families.Count) { $interfaces += [pscustomobject]@{ guid = $guid; alias = $adapter.Name; families = $families } }
        }
        if ($interfaces.Count -eq 0) { throw 'Не найден активный сетевой адаптер с маршрутом по умолчанию.' }
        $service = Get-TunnelService
        $startType = ''
        $wasRunning = $false
        if ($null -ne $service) {
            try { $startType = $service.StartType.ToString(); $wasRunning = $service.Status -eq [ServiceProcess.ServiceControllerStatus]::Running }
            finally { $service.Dispose() }
        }
        $state = [pscustomobject]@{ version = 1; interfaces = $interfaces; warpStartType = $startType; warpRunning = $wasRunning }
        # Persist before stopping a tunnel or changing the first DNS address.
        Write-DNSOnlySnapshot $state
    }
    Stop-ExternalDNS
    $service = Get-TunnelService
    if ($null -ne $service) {
        try {
            Set-Service -Name $ServiceName -StartupType Manual
            if ($service.Status -ne [ServiceProcess.ServiceControllerStatus]::Stopped) {
                Stop-Service -Name $ServiceName
                $service.WaitForStatus([ServiceProcess.ServiceControllerStatus]::Stopped, [TimeSpan]::FromSeconds(30))
            }
        }
        finally { $service.Dispose() }
    }
}

function Set-DNSOnlyInterfaces($State, [bool]$Restore) {
    $adapters = @(Get-NetAdapter -IncludeHidden -ErrorAction Stop)
    $failures = @()
    foreach ($saved in $State.interfaces) {
        $matches = @($adapters | Where-Object { ([Guid]$_.InterfaceGuid) -eq ([Guid]$saved.guid) })
        if ($matches.Count -ne 1) { $failures += "Адаптер $($saved.alias) не найден"; continue }
        foreach ($family in $saved.families) {
            try {
                $inputDNS = @(Get-DnsClientServerAddress -InterfaceIndex $matches[0].ifIndex -AddressFamily $family.name -ErrorAction Stop)
                if ($inputDNS.Count -ne 1) { throw "Не найдены настройки $($family.name)" }
                if ($Restore -and $family.automatic) {
                    Set-DnsClientServerAddress -InputObject $inputDNS[0] -ResetServerAddresses -ErrorAction Stop
                }
                else {
                    $servers = @($family.servers)
                    if (-not $Restore) {
                        $servers = @('127.0.0.1')
                        if ($family.name -eq 'IPv6') { $servers = @('::1') }
                    }
                    Set-DnsClientServerAddress -InputObject $inputDNS[0] -ServerAddresses $servers -ErrorAction Stop
                }
            }
            catch { $failures += "$($saved.alias) $($family.name): $_" }
        }
    }
    Clear-DnsClientCache -ErrorAction SilentlyContinue
    if ($failures.Count) { throw ($failures -join '; ') }
}

function Enable-DNSOnly {
    $state = Read-DNSOnlySnapshot
    if ($null -eq $state) { throw 'Сначала сохраните системные настройки DNS.' }
    $service = Get-Service -Name $DnsServiceName -ErrorAction Stop
    try {
        if ($service.Status -ne [ServiceProcess.ServiceControllerStatus]::Running) { throw 'Служба DNS не запущена. Системные настройки не изменены.' }
    }
    finally { $service.Dispose() }
    Set-DNSOnlyInterfaces $state $false
    Write-Host 'Включён режим «Только DNS». WARP отключён.'
}

function Restore-DNSOnly([switch]$ResumeWarp) {
    $state = Read-DNSOnlySnapshot
    if ($null -eq $state) { return }
    # Keep the proxy running until every original DNS setting is restored.
    # On failure retain the snapshot and service for a safe retry.
    Set-DNSOnlyInterfaces $state $true
    if ($state.warpStartType) {
        $service = Get-TunnelService
        if ($null -ne $service) {
            $service.Dispose()
            Set-Service -Name $ServiceName -StartupType $state.warpStartType
            if ($ResumeWarp -and $state.warpRunning) {
                Start-Service -Name $ServiceName
                (Get-Service -Name $ServiceName).WaitForStatus([ServiceProcess.ServiceControllerStatus]::Running, [TimeSpan]::FromSeconds(30))
            }
        }
    }
    Remove-Item -LiteralPath (Get-DNSOnlySnapshotPath) -Force
    Write-Host 'Прежние системные настройки DNS восстановлены.'
}

# Network/SCM mocks only. No system settings are changed by these tests.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$tokens = $null; $errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot 'warp.ps1'), [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw ($errors | Out-String) }
foreach ($definition in $ast.FindAll({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] }, $false)) { Invoke-Expression $definition.Extent.Text }
. (Join-Path $PSScriptRoot 'dns_only.ps1')
function Assert([bool]$Value, [string]$Message) { if (-not $Value) { throw $Message } }
$StateDirectory = Join-Path ([IO.Path]::GetTempPath()) ('warp-dns-only-test-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $StateDirectory
$ServiceName = 'AmneziaWGTunnel$warp'; $DnsServiceName = 'WarpExternalDNS'; $TunnelName = 'warp'
$script:guid = '{11111111-2222-3333-4444-555555555555}'
$script:index = 10; $script:sets = @(); $script:warp = $false; $script:running = $false
$script:startType = 'Automatic'; $script:proxyRunning = $true; $script:failFamily = ''
$script:static4 = @(); $script:static6 = @('2001:4860:4860::8888')
function Protect-StateDirectory {}
function Protect-StateFile([string]$Path) {}
function Get-NetRoute([object]$ErrorAction) { [pscustomobject]@{ DestinationPrefix = '0.0.0.0/0'; InterfaceIndex = $script:index } }
function Get-NetAdapter([switch]$IncludeHidden, [object]$ErrorAction) {
    [pscustomobject]@{ ifIndex = $script:index; InterfaceGuid = $script:guid; Name = 'Ethernet'; Status = 'Up' }
    # An unrelated connected adapter must not be changed.
    [pscustomobject]@{ ifIndex = 999; InterfaceGuid = '{99999999-2222-3333-4444-555555555555}'; Name = 'Unrelated'; Status = 'Up' }
}
function Get-AdapterDNSOverride([string]$Guid, [string]$Family) {
    Assert ($Guid -eq $script:guid) 'Unexpected adapter snapshotted'
    if ($Family -eq 'IPv4') { return $script:static4 }; return $script:static6
}
function Get-DnsClientServerAddress([int]$InterfaceIndex, [string]$AddressFamily, [object]$ErrorAction) {
    [pscustomobject]@{ InterfaceIndex = $InterfaceIndex; AddressFamily = $AddressFamily }
}
function Set-DnsClientServerAddress($InputObject, [string[]]$ServerAddresses, [switch]$ResetServerAddresses, [object]$ErrorAction) {
    if ($InputObject.AddressFamily -eq $script:failFamily) { throw 'Simulated DNS configuration failure' }
    $script:sets += [pscustomobject]@{ index = $InputObject.InterfaceIndex; family = $InputObject.AddressFamily; reset = [bool]$ResetServerAddresses; servers = @($ServerAddresses) }
}
function Clear-DnsClientCache([object]$ErrorAction) {}
function Stop-ExternalDNS { $script:proxyRunning = $false }
function Get-Service([string]$Name, [object]$ErrorAction) {
    if ($Name -eq $ServiceName -and -not $script:warp) { return $null }
    $running = $script:running
    if ($Name -eq $DnsServiceName) { $running = $script:proxyRunning }
    $status = [ServiceProcess.ServiceControllerStatus]::Stopped
    if ($running) { $status = [ServiceProcess.ServiceControllerStatus]::Running }
    $result = [pscustomobject]@{ Status = $status; StartType = $script:startType; Name = $Name }
    $result | Add-Member ScriptMethod Dispose {}
    $result | Add-Member ScriptMethod WaitForStatus { param($Status, $Timeout) }
    return $result
}
function Set-Service([string]$Name, [string]$StartupType) { $script:startType = $StartupType }
function Stop-Service([string]$Name) { $script:running = $false }
function Start-Service([string]$Name) { $script:running = $true }
try {
    # Windows defaults must allow connection without silently excluding RU/DME.
    foreach ($parameter in @('ExcludedCountries', 'ExcludedNodes')) {
        $definition = @($ast.ParamBlock.Parameters | Where-Object { $_.Name.VariablePath.UserPath -eq $parameter })[0]
        Assert ($definition.DefaultValue.SafeGetValue() -ceq '') "Hidden default filter: $parameter"
    }
    # Explicit user restrictions must still be passed to the helper unchanged.
    $CandidatePath = Join-Path $StateDirectory 'candidate.conf'; $AccountPath = 'account.json'
    $Sample = 3; $Sni = 'ozon.ru'; $Mtu = 1280; $DnsServers = '8.8.8.8'
    $RequiredCountries = ''; $RequiredNodes = ''; $ExcludedCountries = ''; $ExcludedNodes = ''
    function Invoke-Helper([string[]]$Arguments) {
        $script:helperArguments = $Arguments
        [IO.File]::WriteAllText($CandidatePath, "[Interface]`nAddress = 172.16.0.2/32`n[Peer]`nAllowedIPs = 0.0.0.0/0, ::/0`n")
    }
    New-WarpConfiguration
    Assert ('--exclude-country' -notin $script:helperArguments -and '--exclude-node' -notin $script:helperArguments) 'Empty filters restored exclusion defaults'
    $ExcludedCountries = 'RU'; $ExcludedNodes = 'DME'
    New-WarpConfiguration
    Assert ('--exclude-country' -in $script:helperArguments -and 'RU' -in $script:helperArguments -and 'DME' -in $script:helperArguments) 'Explicit exclusions ignored'

    Prepare-DNSOnly
    Assert ($script:sets.Count -eq 0) 'DNS changed before proxy readiness'
    $snapshot = Read-DNSOnlySnapshot
    Assert ($snapshot.interfaces.Count -eq 1) 'Unrelated interface included'
    Assert ($snapshot.interfaces[0].families[0].automatic) 'DHCP DNS was not preserved'
    Assert (-not $snapshot.interfaces[0].families[1].automatic) 'Static IPv6 DNS was lost'
    $script:proxyRunning = $true
    Enable-DNSOnly
    Assert ($script:sets[0].servers[0] -eq '127.0.0.1' -and $script:sets[1].servers[0] -eq '::1') 'Loopback DNS not applied to both address families'
    # Repeated preparation must not overwrite the original DNS with localhost.
    $before = [IO.File]::ReadAllText((Get-DNSOnlySnapshotPath))
    Prepare-DNSOnly
    Assert ([IO.File]::ReadAllText((Get-DNSOnlySnapshotPath)) -ceq $before) 'Original system DNS snapshot overwritten'
    $script:index = 20
    $script:sets = @()
    Restore-DNSOnly
    Assert ($script:sets[0].index -eq 20 -and $script:sets[0].reset) 'DHCP DNS not restored using current adapter index'
    Assert ($script:sets[1].servers[0] -eq '2001:4860:4860::8888') 'Static IPv6 DNS not restored'
    Assert (-not (Test-Path -LiteralPath (Get-DNSOnlySnapshotPath))) 'Completed snapshot remains'

    $script:warp = $true; $script:running = $true; $script:startType = 'Automatic'
    $script:static4 = @('9.9.9.9', '149.112.112.112'); $script:static6 = @()
    Prepare-DNSOnly
    Assert (-not $script:running -and $script:startType -eq 'Manual') 'WARP can auto-start alongside DNS-only'
    $script:proxyRunning = $true; $script:failFamily = 'IPv6'
    $failed = $false
    try { Enable-DNSOnly } catch { $failed = $true }
    Assert $failed 'Partial DNS apply failure not reported'
    $failed = $false
    try { Restore-DNSOnly } catch { $failed = $true }
    Assert ($failed -and (Test-Path -LiteralPath (Get-DNSOnlySnapshotPath)) -and $script:proxyRunning) 'Failed restore lost recovery snapshot/proxy'
    # Uninstall must stop before deleting the resolver if DNS restore fails.
    $script:deletedDNS = $false
    function Remove-ExternalDNS { $script:deletedDNS = $true }
    try { Remove-WarpCompletely } catch {}
    Assert (-not $script:deletedDNS) 'Uninstall removed resolver before restoring system DNS'
    $script:failFamily = ''; $script:sets = @()
    Restore-DNSOnly -ResumeWarp
    Assert ($script:running -and $script:startType -eq 'Automatic') 'Failed activation did not restore prior WARP startup/state'
    Assert (($script:sets[0].servers -join ',') -ceq '9.9.9.9,149.112.112.112') 'Static IPv4 DNS not restored'
    Assert $script:sets[1].reset 'Automatic IPv6 DNS not restored'
    'Standalone DNS and endpoint filter tests OK'
}
finally { Remove-Item -LiteralPath $StateDirectory -Recurse -Force }

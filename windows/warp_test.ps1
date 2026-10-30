# Run with pwsh -NoProfile -File windows/warp_test.ps1; does not require Windows or administrator rights.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$tokens = $null
$errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot 'warp.ps1'), [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw ($errors | Out-String) }
foreach ($definition in $ast.FindAll({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] }, $false)) {
    Invoke-Expression $definition.Extent.Text
}
. (Join-Path $PSScriptRoot 'dns_only.ps1')
function Assert([bool]$Value, [string]$Message) { if (-not $Value) { throw $Message } }
$example = "[Interface]`r`nPrivateKey = test`r`n  DNS = 8.8.8.8`r`n[Peer]`r`nEndpoint = 1.2.3.4:2408`r`n"
$expected = "[Interface]`r`nDNS = 127.0.0.1`r`nPrivateKey = test`r`n[Peer]`r`nEndpoint = 1.2.3.4:2408`r`n"
Assert ((Set-ConfigurationDNS $example '127.0.0.1') -ceq $expected) 'DNS replacement/newline preservation failed'
$noDns = "[Interface]`nPrivateKey = test`n[Peer]`nEndpoint = 1.2.3.4:2408"
Assert ((Set-ConfigurationDNS $noDns '8.8.8.8, 2001:4860:4860::8888') -match '(?m)^DNS = 8\.8\.8\.8, 2001:4860:4860::8888$') 'DNS insertion failed'
foreach ($invalid in @('', '8.8.8.999', '1', 'example.com', '8.8.8.8,', "8.8.8.8`n[Peer]")) {
    $rejected = $false
    try { $null = Get-ValidatedDNS $invalid } catch { $rejected = $true }
    Assert $rejected "Invalid DNS accepted: $invalid"
}
foreach ($invalidConfig in @('[Peer]', "[Interface]`n[Interface]")) {
    $rejected = $false
    try { $null = Set-ConfigurationDNS $invalidConfig '127.0.0.1' } catch { $rejected = $true }
    Assert $rejected 'Malformed config accepted'
}

# Mock SCM while performing real candidate/backup atomic file replacements.
$StateDirectory = Join-Path ([IO.Path]::GetTempPath()) ('warp-dns-ps-test-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $StateDirectory
$ConfigPath = Join-Path $StateDirectory 'warp.conf'
$DnsServers = '127.0.0.1'
$ServiceName = 'Tunnel'
$DnsServiceName = 'DNS'
$script:serviceStates = @{}
$script:failStartCount = 0
function Protect-StateFile([string]$Path) {}
function Get-Service([string]$Name, [object]$ErrorAction) {
    if (-not $script:serviceStates.ContainsKey($Name)) { return $null }
    $service = [pscustomobject]@{ Name = $Name; Status = $script:serviceStates[$Name] }
    $service | Add-Member -MemberType ScriptMethod -Name Dispose -Value {}
    $service | Add-Member -MemberType ScriptMethod -Name WaitForStatus -Value {
        param($Status, $Timeout)
        if ($script:serviceStates[$this.Name] -ne $Status) { throw 'Mock wait failure' }
    }
    return $service
}
function Stop-Service([string]$Name) { $script:serviceStates[$Name] = [ServiceProcess.ServiceControllerStatus]::Stopped }
function Start-Service([string]$Name) {
    if ($script:failStartCount -gt 0 -and $Name -eq 'Tunnel') { $script:failStartCount--; throw 'Simulated tunnel startup failure' }
    $script:serviceStates[$Name] = [ServiceProcess.ServiceControllerStatus]::Running
}
try {
    foreach ($running in @($false, $true)) {
        [IO.File]::WriteAllText($ConfigPath, $example)
        $script:serviceStates = @{ Tunnel = [ServiceProcess.ServiceControllerStatus]::Stopped; DNS = [ServiceProcess.ServiceControllerStatus]::Stopped }
        if ($running) { $script:serviceStates['Tunnel'] = [ServiceProcess.ServiceControllerStatus]::Running; $script:serviceStates['DNS'] = [ServiceProcess.ServiceControllerStatus]::Running }
        Set-TunnelDNS
        Assert ([IO.File]::ReadAllText($ConfigPath) -ceq $expected) 'ApplyDNS did not replace file'
        Assert (($script:serviceStates['Tunnel'] -eq [ServiceProcess.ServiceControllerStatus]::Running) -eq $running) 'ApplyDNS changed previous tunnel running state'
        Assert ($script:serviceStates['DNS'] -eq [ServiceProcess.ServiceControllerStatus]::Stopped) 'Proxy must remain stopped for GUI configuration'
    }
    [IO.File]::WriteAllText($ConfigPath, $example)
    $script:serviceStates = @{ Tunnel = [ServiceProcess.ServiceControllerStatus]::Running; DNS = [ServiceProcess.ServiceControllerStatus]::Running }
    $script:failStartCount = 1
    $rejected = $false
    try { Set-TunnelDNS } catch { $rejected = $true }
    Assert $rejected 'ApplyDNS must report startup failure'
    Assert ([IO.File]::ReadAllText($ConfigPath) -ceq $example) 'ApplyDNS rollback did not restore original file'
    Assert ($script:serviceStates['Tunnel'] -eq [ServiceProcess.ServiceControllerStatus]::Running) 'Rollback did not restore tunnel state'
    Assert ($script:serviceStates['DNS'] -eq [ServiceProcess.ServiceControllerStatus]::Running) 'Rollback did not restore proxy state'
    Assert (@(Get-ChildItem $StateDirectory).Count -eq 1) 'Candidate/backup leaked after successful rollback'

    [IO.File]::WriteAllText($ConfigPath, $example)
    $script:serviceStates = @{ Tunnel = [ServiceProcess.ServiceControllerStatus]::Running; DNS = [ServiceProcess.ServiceControllerStatus]::Running }
    $script:failStartCount = 2
    $rejected = $false
    try { Set-TunnelDNS } catch { $rejected = $_.ToString() -match 'Ошибка восстановления' }
    Assert $rejected 'Double failure must report rollback failure'
    Assert ([IO.File]::ReadAllText($ConfigPath) -ceq $example) 'Double startup failure must retain original config'
    # Exercise Install/Rescan rollback after the new localhost DNS config has
    # already replaced the old file, but the tunnel cannot start.
    $CandidatePath = Join-Path $StateDirectory 'warp.candidate.conf'
    $script:failInstallCount = 0
    function Remove-TunnelService {
        Stop-ExternalDNS
        $script:serviceStates.Remove('Tunnel')
    }
    function Install-TunnelService {
        $script:serviceStates['Tunnel'] = [ServiceProcess.ServiceControllerStatus]::Running
        if ($script:failInstallCount -gt 0) {
            $script:failInstallCount--
            throw 'Simulated partial tunnel install failure'
        }
    }
    foreach ($running in @($false, $true)) {
        [IO.File]::WriteAllText($ConfigPath, $example)
        [IO.File]::WriteAllText($CandidatePath, $expected)
        $script:serviceStates = @{ Tunnel = [ServiceProcess.ServiceControllerStatus]::Stopped; DNS = [ServiceProcess.ServiceControllerStatus]::Stopped }
        if ($running) {
            $script:serviceStates['Tunnel'] = [ServiceProcess.ServiceControllerStatus]::Running
            $script:serviceStates['DNS'] = [ServiceProcess.ServiceControllerStatus]::Running
        }
        $script:failStartCount = 0
        $script:failInstallCount = 1
        $rejected = $false
        try { Install-TunnelConfiguration } catch { $rejected = $true }
        Assert $rejected 'Partial install must report failure'
        Assert ([IO.File]::ReadAllText($ConfigPath) -ceq $example) 'Install rollback lost old configuration'
        Assert (($script:serviceStates['Tunnel'] -eq [ServiceProcess.ServiceControllerStatus]::Running) -eq $running) 'Install rollback changed tunnel running state'
        Assert (($script:serviceStates['DNS'] -eq [ServiceProcess.ServiceControllerStatus]::Running) -eq $running) 'Install rollback changed proxy running state'
    }
    Remove-Item -LiteralPath $ConfigPath
    [IO.File]::WriteAllText($CandidatePath, $expected)
    $script:serviceStates = @{}
    $script:failInstallCount = 1
    $rejected = $false
    try { Install-TunnelConfiguration } catch { $rejected = $true }
    Assert $rejected 'Failed first install must report failure'
    Assert (-not (Test-Path -LiteralPath $ConfigPath)) 'Failed first install left localhost DNS config'
    Assert (-not $script:serviceStates.ContainsKey('Tunnel')) 'Failed first install left partial service'

    [IO.File]::WriteAllText($CandidatePath, $expected)
    $script:failInstallCount = 0
    Install-TunnelConfiguration
    Assert ([IO.File]::ReadAllText($ConfigPath) -ceq $expected) 'Successful install lost new config'
    Assert ($script:serviceStates['Tunnel'] -eq [ServiceProcess.ServiceControllerStatus]::Running) 'Successful install did not start tunnel'

    $HelperPath = Join-Path $StateDirectory 'warp-helper.exe'
    [IO.File]::WriteAllText($HelperPath, 'embedded helper test')
    $BundledHelperHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $HelperPath).Hash
    Install-Helper
    [IO.File]::WriteAllText($HelperPath, 'changed after extraction')
    $rejected = $false
    try { Install-Helper } catch { $rejected = $_.ToString() -match 'SHA-256' }
    Assert $rejected 'Modified bundled helper must not be executed'
    $BundledHelperHash = ''
    $rejected = $false
    try { Install-Helper } catch { $rejected = $true }
    Assert $rejected 'Missing bundled helper checksum must be rejected'
    # Rescanning through our own active full tunnel must be avoided. Restore
    # the prior tunnel/DNS when account setup or endpoint scanning fails.
    function Ensure-Account {
        Assert (-not $script:serviceStates.ContainsKey('Tunnel') -or $script:serviceStates['Tunnel'] -eq [ServiceProcess.ServiceControllerStatus]::Stopped) 'Registration nested inside the old WARP tunnel'
    }
    function New-WarpConfiguration { throw 'Simulated endpoint scan failure' }
    function Start-ExternalDNS { $script:serviceStates['DNS'] = [ServiceProcess.ServiceControllerStatus]::Running }
    foreach ($running in @($false, $true)) {
        $script:serviceStates = @{ Tunnel = [ServiceProcess.ServiceControllerStatus]::Stopped; DNS = [ServiceProcess.ServiceControllerStatus]::Stopped }
        if ($running) { $script:serviceStates['Tunnel'] = [ServiceProcess.ServiceControllerStatus]::Running; $script:serviceStates['DNS'] = [ServiceProcess.ServiceControllerStatus]::Running }
        $script:failStartCount = 0
        $rejected = $false
        try { Update-WarpTunnel } catch { $rejected = $_.ToString() -match 'Simulated endpoint scan failure' }
        Assert $rejected 'Endpoint search failure not reported'
        Assert (($script:serviceStates['Tunnel'] -eq [ServiceProcess.ServiceControllerStatus]::Running) -eq $running) 'Failed search changed previous tunnel state'
        Assert (($script:serviceStates['DNS'] -eq [ServiceProcess.ServiceControllerStatus]::Running) -eq $running) 'Failed search changed previous DNS state'
    }
    function New-WarpConfiguration {}
    function Install-TunnelConfiguration { $script:serviceStates['Tunnel'] = [ServiceProcess.ServiceControllerStatus]::Running }
    Update-WarpTunnel
    Assert ($script:serviceStates['Tunnel'] -eq [ServiceProcess.ServiceControllerStatus]::Running) 'Successful rescan did not replace the tunnel'
    'PowerShell DNS, scan rollback, installation rollback and embedded helper tests OK'
}
finally { Remove-Item -LiteralPath $StateDirectory -Recurse -Force }

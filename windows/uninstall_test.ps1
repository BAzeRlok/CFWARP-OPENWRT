# Uses temporary fixture directories and mock MSI/SCM; never uninstalls software.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$tokens = $null
$errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot 'warp.ps1'), [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw ($errors | Out-String) }
foreach ($definition in $ast.FindAll({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] }, $false)) { Invoke-Expression $definition.Extent.Text }
function Assert([bool]$Value, [string]$Message) { if (-not $Value) { throw $Message } }
$fixture = Join-Path ([IO.Path]::GetTempPath()) ('warp-uninstall-test-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $fixture
$StateDirectory = Join-Path $fixture 'state'
$AmneziaPath = Join-Path $fixture 'backend/amneziawg.exe'
$env:LOCALAPPDATA = Join-Path $fixture 'local'
$env:APPDATA = Join-Path $fixture 'roaming'
$script:tempRoot = Join-Path $fixture 'temp'
$script:msiCode = 0
$script:msiCalls = @()
$script:deletedServices = @()
$script:states = @{}
function Get-WarpTemporaryDirectory { return $script:tempRoot }
function Get-AmneziaProducts { return '{11111111-2222-3333-4444-555555555555}' }
function Start-Process([string]$FilePath, [object[]]$ArgumentList, [switch]$Wait, [switch]$PassThru) {
    Assert ($FilePath -eq 'msiexec.exe' -and $Wait -and $PassThru) 'Uninstall invoked unexpected process'
    Assert ($ArgumentList[0] -eq '/x' -and $ArgumentList -contains '/norestart') 'MSI invocation must uninstall without reboot'
    $script:msiCalls += $ArgumentList[1]
    return [pscustomobject]@{ ExitCode = $script:msiCode }
}
function Get-Service([string[]]$Name, [object]$ErrorAction) {
    foreach ($key in @($script:states.Keys)) {
        if (-not @($Name | Where-Object { $key -like $_ }).Count) { continue }
        $result = [pscustomobject]@{Name=$key; Status=$script:states[$key]}
        $result | Add-Member -MemberType ScriptMethod -Name Dispose -Value {}
        $result | Add-Member -MemberType ScriptMethod -Name WaitForStatus -Value {
            param($status, $timeout)
            Assert ($script:states[$this.Name] -eq $status) 'Service did not stop'
        }
        $result
    }
}
function Stop-Service([string]$Name, [switch]$Force) { $script:states[$Name] = [ServiceProcess.ServiceControllerStatus]::Stopped }
function sc.exe([string]$Action, [string]$Name) {
    Assert ($Action -eq 'delete') 'Unexpected sc operation'
    $script:deletedServices += $Name
    $script:states.Remove($Name)
    $global:LASTEXITCODE = 0
}
function Get-Process { return @() }
function Remove-ExternalDNS { $script:states.Remove('WarpExternalDNS') }
function Remove-TunnelService { $script:states.Remove('AmneziaWGTunnel$warp') }
try {
    $roots = @(Get-WarpCleanupRoots)
    foreach ($path in @($roots) + @($script:tempRoot)) {
        $null = New-Item -ItemType Directory -Path $path -Force
        [IO.File]::WriteAllText((Join-Path $path 'keep-or-remove.txt'), 'fixture')
    }
    [IO.File]::WriteAllText($AmneziaPath, 'not executable')
    $unrelated = Join-Path $fixture 'unrelated'
    $null = New-Item -ItemType Directory -Path $unrelated
    $sentinel = Join-Path $unrelated 'keep.txt'
    [IO.File]::WriteAllText($sentinel, 'do not delete')
    $rejected = $false
    try { Remove-WarpOwnedTree $fixture } catch { $rejected = $true }
    Assert $rejected 'Cleanup allowed broad parent directory'
    Assert (Test-Path -LiteralPath $sentinel) 'Unrelated data removed'
    foreach ($service in @('WireGuardManager', 'WireGuardTunnel$home', 'SomeOtherService')) {
        $rejected = $false
        try { Remove-AmneziaService $service } catch { $rejected = $true }
        Assert $rejected 'Cleanup accepted unrelated service'
    }
    $rejected = $false
    try { Remove-AmneziaProduct 'not-a-guid' } catch { $rejected = $true }
    Assert ($rejected -and $script:msiCalls.Count -eq 0) 'Invalid MSI product accepted'

    # If MSI fails, state/profiles must remain available for retry/recovery.
    $script:msiCode = 1603
    $rejected = $false
    try { Remove-WarpCompletely } catch { $rejected = $true }
    Assert $rejected 'MSI failure reported as success'
    Assert (Test-Path -LiteralPath (Join-Path $StateDirectory 'keep-or-remove.txt')) 'State wiped after MSI failure'

    $stale = Join-Path $script:tempRoot 'warp-windows-123456'
    $otherTemp = Join-Path $script:tempRoot 'warp-windows-not-owned'
    $null = New-Item -ItemType Directory -Path $stale, $otherTemp
    $msi = Join-Path $script:tempRoot 'amneziawg-amd64-3.1.0.msi'
    $otherMsi = Join-Path $script:tempRoot 'unrelated.msi'
    [IO.File]::WriteAllText($msi, 'remove')
    [IO.File]::WriteAllText($otherMsi, 'keep')
    $link = Join-Path $StateDirectory 'external-link'
    $null = New-Item -ItemType SymbolicLink -Path $link -Target $unrelated
    $script:states = @{
        'WarpExternalDNS' = [ServiceProcess.ServiceControllerStatus]::Running
        'AmneziaWGManager' = [ServiceProcess.ServiceControllerStatus]::Running
        'AmneziaWGTunnel$warp' = [ServiceProcess.ServiceControllerStatus]::Running
        'AmneziaWGTunnel$other' = [ServiceProcess.ServiceControllerStatus]::Running
        'WireGuardTunnel$home' = [ServiceProcess.ServiceControllerStatus]::Running
    }
    $script:msiCode = 3010
    Remove-WarpCompletely
    Assert $script:UninstallNeedsReboot 'MSI reboot-required code lost'
    foreach ($root in $roots) { Assert (-not (Test-Path -LiteralPath $root)) "Owned folder remains: $root" }
    Assert (-not (Test-Path -LiteralPath $stale)) 'Stale client temp remains'
    Assert (-not (Test-Path -LiteralPath $msi)) 'Downloaded MSI remains'
    Assert (Test-Path -LiteralPath $sentinel) 'Cleanup followed symlink into unrelated data'
    Assert (Test-Path -LiteralPath $otherTemp) 'Unrelated temporary directory removed'
    Assert (Test-Path -LiteralPath $otherMsi) 'Unrelated MSI removed'
    Assert ($script:states.ContainsKey('WireGuardTunnel$home')) 'Unrelated WireGuard service removed'
    Assert ($script:deletedServices -contains 'AmneziaWGManager') 'Backend manager not removed'
    Assert ($script:deletedServices -contains 'AmneziaWGTunnel$other') 'Backend tunnel not removed'
    'Full uninstall tests OK'
}
finally { Remove-Item -LiteralPath $fixture -Recurse -Force }

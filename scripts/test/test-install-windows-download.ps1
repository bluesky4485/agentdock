[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string] $Url,
    [Parameter(Mandatory = $true)]
    [string] $ExpectedInstallerPath,
    [Parameter(Mandatory = $true)]
    [string] $OutputPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$resolvedExpected = Resolve-Path -LiteralPath $ExpectedInstallerPath
$downloaded = $false
for ($attempt = 1; $attempt -le 10; $attempt++) {
    try {
        Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $OutputPath
        $downloaded = $true
        break
    } catch {
        if ($attempt -eq 10) {
            throw
        }
        Start-Sleep -Seconds 2
    }
}
if (-not $downloaded) {
    throw "Unable to download Windows installer: $Url"
}

$expectedContent = [IO.File]::ReadAllText($resolvedExpected).Replace("`r`n", "`n")
$downloadedContent = [IO.File]::ReadAllText($OutputPath).Replace("`r`n", "`n")
# 发布流程会把仓库安装脚本里的上游 release base 重写成实际发布仓库（见
# release.yml publish job）。对比前对仓库版本做同一重写：既接受发布期这一处
# 已知差异，又保证安装脚本的其他任何改动仍会被这里拦下。
$upstreamReleaseBase = 'https://github.com/uvwt/agentdock/releases'
if ($downloadedContent -ne $expectedContent -and $Url -match '^(https://github\.com/[^/]+/[^/]+)/releases/') {
    $publishedReleaseBase = $Matches[1] + '/releases'
    $expectedContent = $expectedContent.Replace($upstreamReleaseBase, $publishedReleaseBase)
}
if (-not [string]::Equals($expectedContent, $downloadedContent, [StringComparison]::Ordinal)) {
    throw 'Downloaded installer content does not match the repository version after line-ending normalization.'
}

$downloadedHash = (Get-FileHash -LiteralPath $OutputPath -Algorithm SHA256).Hash
& (Join-Path $PSScriptRoot 'test-install-windows.ps1') -InstallerPath $OutputPath
Write-Host "Downloaded Windows installer validation passed: $downloadedHash"

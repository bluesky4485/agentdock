[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string[]] $Path,
    [switch] $VerifyOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-ExpectedSigningCertificate {
    if ([string]::IsNullOrWhiteSpace($env:WINDOWS_SIGNING_CERT_BASE64)) {
        return $null
    }
    if ([string]::IsNullOrWhiteSpace($env:WINDOWS_SIGNING_CERT_PASSWORD)) {
        throw 'WINDOWS_SIGNING_CERT_PASSWORD is required when WINDOWS_SIGNING_CERT_BASE64 is configured.'
    }

    $bytes = [Convert]::FromBase64String($env:WINDOWS_SIGNING_CERT_BASE64)
    return [System.Security.Cryptography.X509Certificates.X509Certificate2]::new(
        $bytes,
        $env:WINDOWS_SIGNING_CERT_PASSWORD,
        [System.Security.Cryptography.X509Certificates.X509KeyStorageFlags]::EphemeralKeySet
    )
}

function Test-IsExpectedSelfSignedTrustFailure {
    param(
        [Parameter(Mandatory = $true)]
        $Signature,
        [Parameter(Mandatory = $true)]
        [string] $SignToolOutput
    )

    if (-not $Signature.SignerCertificate) {
        return $false
    }
    if ($Signature.SignerCertificate.Subject -ne $Signature.SignerCertificate.Issuer) {
        return $false
    }
    if (@('UnknownError', 'NotTrusted') -notcontains [string] $Signature.Status) {
        return $false
    }

    $trustPattern = '(?is)certificate chain processed.*terminated in a root.*not trusted|certificate chain.*not trusted|untrustedroot'
    return (
        ([string] $Signature.StatusMessage -match $trustPattern) -and
        ($SignToolOutput -match $trustPattern)
    )
}

$signTool = Get-ChildItem "${env:ProgramFiles(x86)}\Windows Kits\10\bin\*\x64\signtool.exe" |
    Sort-Object FullName |
    Select-Object -Last 1
if (-not $signTool) {
    throw 'signtool.exe was not found.'
}

$resolvedPaths = @($Path | ForEach-Object {
    (Resolve-Path -LiteralPath $_ -ErrorAction Stop).Path
})
$expectedCertificate = Get-ExpectedSigningCertificate

# 未配置证书时，VerifyOnly 只确认"文件存在且刻意未签名"，直接通过；
# 签名路径仍然必须报错，避免把未签名产物误当成已签名发布。
if (-not $expectedCertificate -and $VerifyOnly) {
    foreach ($item in $resolvedPaths) {
        if (-not (Test-Path -LiteralPath $item -PathType Leaf)) {
            throw "Artifact to verify is missing: $item"
        }
        $signature = Get-AuthenticodeSignature -LiteralPath $item
        if ($signature.SignerCertificate) {
            throw "WINDOWS_SIGNING_CERT_BASE64 is not configured, but $item already carries an Authenticode signature."
        }
        Write-Host "Skipping Authenticode verification for $item: no signing certificate configured (unsigned release build)."
    }
    exit 0
}

try {
    if (-not $VerifyOnly) {
        if (-not $expectedCertificate) {
            throw 'WINDOWS_SIGNING_CERT_BASE64 is required.'
        }

        $pfx = Join-Path $env:RUNNER_TEMP ("agentdock-signing-" + [Guid]::NewGuid().ToString('N') + '.pfx')
        [IO.File]::WriteAllBytes($pfx, [Convert]::FromBase64String($env:WINDOWS_SIGNING_CERT_BASE64))
        try {
            foreach ($item in $resolvedPaths) {
                & $signTool.FullName sign `
                    /fd SHA256 `
                    /f $pfx `
                    /p $env:WINDOWS_SIGNING_CERT_PASSWORD `
                    /tr http://timestamp.digicert.com `
                    /td SHA256 `
                    $item
                if ($LASTEXITCODE -ne 0) {
                    throw "signtool sign failed for $item with exit code $LASTEXITCODE."
                }
            }
        } finally {
            Remove-Item -LiteralPath $pfx -Force -ErrorAction SilentlyContinue
        }
    }

    foreach ($item in $resolvedPaths) {
        $initialSignature = Get-AuthenticodeSignature -LiteralPath $item
        if (-not $initialSignature.SignerCertificate) {
            throw "Authenticode signer certificate is missing for $item."
        }
        if ($expectedCertificate -and
            $initialSignature.SignerCertificate.Thumbprint -ne $expectedCertificate.Thumbprint) {
            throw "Authenticode signer certificate does not match the configured certificate for $item."
        }

        Write-Host "Verifying Authenticode signature: $item"
        $verifyOutput = @(& $signTool.FullName verify /pa /all /v $item 2>&1)
        $verifyExitCode = $LASTEXITCODE
        $verifyText = $verifyOutput | Out-String
        $verifyOutput | ForEach-Object { Write-Host $_ }

        $signature = Get-AuthenticodeSignature -LiteralPath $item
        if ($expectedCertificate -and
            $signature.SignerCertificate.Thumbprint -ne $expectedCertificate.Thumbprint) {
            throw "Verified Authenticode signer certificate does not match the configured certificate for $item."
        }

        if ($verifyExitCode -eq 0 -and $signature.Status -eq 'Valid') {
            continue
        }

        if ($expectedCertificate -and
            (Test-IsExpectedSelfSignedTrustFailure -Signature $signature -SignToolOutput $verifyText)) {
            Write-Warning "Accepted self-signed AgentDock certificate for ${item}: signer identity and file signature are valid, but Windows does not trust the certificate chain."
            $global:LASTEXITCODE = 0
            continue
        }

        if ($verifyExitCode -ne 0) {
            throw "signtool verify failed for $item with exit code $verifyExitCode."
        }
        throw "Authenticode signature is not valid for ${item}: $($signature.Status)"
    }
} finally {
    if ($expectedCertificate) {
        $expectedCertificate.Dispose()
    }
}

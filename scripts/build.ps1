param(
    [ValidateSet('docker', 'podman')]
    [string]$Engine,
    [ValidateSet('linux/amd64', 'linux/arm64')]
    [string]$Platform = 'linux/amd64'
)

$ErrorActionPreference = 'Stop'

function Invoke-BuildCommand {
    param([string]$Command, [string[]]$Arguments)

    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Command $($Arguments -join ' ') failed (exit $LASTEXITCODE)."
    }
}

function Get-BuildIdentity {
    param([string]$Root)

    $sha = (Invoke-BuildCommand git @('-C', $Root, 'rev-parse', '--verify', 'HEAD^{commit}')).Trim()
    $epoch = (Invoke-BuildCommand git @('-C', $Root, 'show', '-s', '--format=%ct', $sha)).Trim()
    $status = Invoke-BuildCommand git @('-C', $Root, 'status', '--porcelain')
    $dirty = [bool]$status
    if ($dirty -and $env:ALLOW_DIRTY -ne '1') {
        throw "Working tree has uncommitted changes. Commit them first, or set ALLOW_DIRTY=1 to create a .dirty build."
    }
    if ($sha -notmatch '^[0-9a-f]{40,64}$' -or $epoch -notmatch '^\d+$') {
        throw 'Invalid Git commit metadata.'
    }
    $datetime = [DateTimeOffset]::FromUnixTimeSeconds([long]$epoch).UtcDateTime.ToString(
        "yyyyMMdd'T'HHmmss'Z'", [Globalization.CultureInfo]::InvariantCulture)
    $version = "$datetime-g$($sha.Substring(0, 12))"
    if ($dirty) { $version += '.dirty' }
    return $version
}

function Invoke-ImageBuild {
    param([string]$Root, [string]$Engine, [string]$Platform)

    $version = Get-BuildIdentity $Root
    if (-not $Engine) {
        if (Get-Command docker -ErrorAction SilentlyContinue) { $Engine = 'docker' }
        elseif (Get-Command podman -ErrorAction SilentlyContinue) { $Engine = 'podman' }
        else { throw 'Install Docker Desktop or Podman before building.' }
    }

    $versionTag = "localhost/pandadays:$version"
    $latestTag = 'localhost/pandadays:latest'
    $arguments = if ($Engine -eq 'docker') {
        @('buildx', 'build', '--load')
    } else {
        @('build', '--format', 'docker')
    }
    $arguments += @('--platform', $Platform, '--build-arg', "VERSION=$version",
        '--tag', $versionTag, '--tag', $latestTag, $Root)
    Write-Host "Building $versionTag and $latestTag for $Platform"
    Invoke-BuildCommand $Engine $arguments

    $archive = Join-Path $Root 'dist\pandadays-image.tar'
    New-Item -ItemType Directory -Path (Split-Path $archive) -Force | Out-Null
    Invoke-BuildCommand $Engine @('save', '--output', $archive, $versionTag, $latestTag)
    Write-Host "Saved both image tags to $archive"
}

if ($MyInvocation.InvocationName -ne '.') {
    try {
        Invoke-ImageBuild -Root (Split-Path $PSScriptRoot) -Engine $Engine -Platform $Platform
    } catch {
        [Console]::Error.WriteLine("[pandadays:build] $($_.Exception.Message)")
        exit 1
    }
}
